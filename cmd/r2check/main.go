// Command r2check tests the Cloudflare R2 settings the same way the server
// uses them, and prints what works and what does not. It never prints a key.
//
//	set -a; . ./.env; set +a
//	go run ./cmd/r2check
//
// It reads the server's own variables (R2_ACCOUNT_ID, R2_BUCKET,
// R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_PUBLIC_URL), then:
//
//  1. uploads a 1x1 picture through the server's storage code,
//  2. reads it back from the public address,
//  3. asks for a presigned upload and tries it with the right size (must work)
//     and a wrong size (must be refused),
//  4. deletes everything it made.
//
// The test files are under the owner name "r2check" and live for seconds.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/beamMiter/HomeHQ-server/internal/config"
	"github.com/beamMiter/HomeHQ-server/internal/storage"
)

// A 1x1 PNG.
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

type report struct{ failed bool }

func (r *report) ok(format string, a ...any) { fmt.Printf("  ✓ "+format+"\n", a...) }
func (r *report) bad(format string, a ...any) {
	r.failed = true
	fmt.Printf("  ✗ "+format+"\n", a...)
}
func (r *report) note(format string, a ...any) { fmt.Printf("  • "+format+"\n", a...) }

// hint turns the usual R2 failures into what to check, without echoing
// anything from the error that could hold a secret (SDK errors do not, but the
// message is only matched here, never printed as advice).
func hint(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "InvalidAccessKeyId"):
		return "Access Key ID is wrong (not the Token value; copy the Access Key ID)"
	case strings.Contains(s, "SignatureDoesNotMatch"):
		return "Secret Access Key is wrong, or has a stray space/newline in it"
	case strings.Contains(s, "AccessDenied"), strings.Contains(s, "Forbidden"), strings.Contains(s, "403"):
		return "the token is not allowed on this bucket: it needs Object Read & Write scoped to the bucket"
	case strings.Contains(s, "NoSuchBucket"):
		return "bucket name is wrong, or the bucket is in another Cloudflare account"
	case strings.Contains(s, "no such host"), strings.Contains(s, "dial tcp"):
		return "R2_ACCOUNT_ID is probably wrong (the endpoint is built from it), or there is no network"
	}
	return ""
}

func main() {
	cfg := config.Load()
	var r report
	fmt.Println("R2 check")

	// 0. Are the settings there? Names only, never values.
	required := map[string]string{
		"R2_ACCOUNT_ID":        cfg.R2AccountID,
		"R2_BUCKET":            cfg.R2Bucket,
		"R2_ACCESS_KEY_ID":     cfg.R2AccessKey,
		"R2_SECRET_ACCESS_KEY": cfg.R2SecretKey,
		"R2_PUBLIC_URL":        cfg.R2PublicURL,
	}
	missing := false
	for _, name := range []string{"R2_ACCOUNT_ID", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PUBLIC_URL"} {
		v := required[name]
		switch {
		case v == "":
			r.bad("%s is not set", name)
			missing = true
		case strings.TrimSpace(v) != v:
			r.bad("%s has a space or newline at the start or end", name)
			missing = true
		default:
			r.ok("%s is set", name)
		}
	}
	if cfg.StorageBackend != "r2" {
		r.note("STORAGE_BACKEND is %q here: the server only uses R2 when it is \"r2\" (this check ignores that)", cfg.StorageBackend)
	}
	if !strings.HasPrefix(cfg.R2PublicURL, "https://") {
		r.bad("R2_PUBLIC_URL should start with https://")
		missing = true
	}
	if missing {
		fmt.Println("\nFix the lines above, then run it again.")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := storage.NewR2(cfg)
	if err != nil {
		fmt.Println("cannot start the R2 client:", err)
		os.Exit(1)
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	publicBase := strings.TrimRight(cfg.R2PublicURL, "/")

	var cleanup []string
	defer func() {
		for _, key := range cleanup {
			if err := st.Delete(context.Background(), key); err != nil {
				fmt.Printf("  ✗ could not delete the test file %s: %v\n", key, err)
			}
		}
	}()

	// 1. Upload through the server's own code.
	fmt.Println("\n1. upload through the server's storage code")
	res, err := st.Save(ctx, "r2check", bytes.NewReader(tinyPNG), "check.png", "image/png", int64(len(tinyPNG)))
	if err != nil {
		r.bad("upload failed: %v", err)
		if h := hint(err); h != "" {
			r.note("%s", h)
		}
		fmt.Println("\nNothing else can be tested until upload works.")
		os.Exit(1)
	}
	key := strings.TrimPrefix(res.URL, publicBase+"/")
	cleanup = append(cleanup, key)
	r.ok("uploaded %d bytes", len(tinyPNG))

	// 2. Read it back from the public address.
	fmt.Println("\n2. read it back from R2_PUBLIC_URL")
	resp, err := hc.Get(res.URL)
	if err != nil {
		r.bad("request failed: %v", err)
	} else {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && bytes.Equal(body, tinyPNG):
			r.ok("HTTP 200 and the same bytes (content-type %s)", resp.Header.Get("Content-Type"))
		case resp.StatusCode == http.StatusOK:
			r.bad("HTTP 200 but the bytes differ: R2_PUBLIC_URL may point at another bucket")
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
			r.bad("HTTP %d: public access is probably not enabled for the bucket (Settings → Public Development URL → Enable), or R2_PUBLIC_URL is another bucket's", resp.StatusCode)
		default:
			r.bad("HTTP %d", resp.StatusCode)
		}
	}

	// 3. Presigned upload: the size is part of the signature.
	fmt.Println("\n3. presigned upload (the app's direct-upload path)")
	ps, err := st.PresignPut(ctx, "r2check", "presign.png", "image/png", int64(len(tinyPNG)))
	if err != nil {
		r.bad("could not sign an upload: %v", err)
	} else {
		if u, perr := url.Parse(ps.URL); perr == nil {
			pkey := strings.TrimPrefix(u.Path, "/")
			pkey = strings.TrimPrefix(pkey, cfg.R2Bucket+"/") // path-style addresses carry the bucket
			cleanup = append(cleanup, pkey)
		}
		put := func(body []byte) (int, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, ps.URL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "image/png")
			resp, err := hc.Do(req)
			if err != nil {
				return 0, err
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp.StatusCode, nil
		}
		if code, err := put(tinyPNG[:len(tinyPNG)-1]); err != nil {
			r.bad("wrong-size upload: request failed: %v", err)
		} else if code >= 200 && code < 300 {
			r.bad("a wrong-size upload was ACCEPTED (HTTP %d): the signed size is not enforced", code)
		} else {
			r.ok("a wrong-size upload is refused (HTTP %d)", code)
		}
		if code, err := put(tinyPNG); err != nil {
			r.bad("right-size upload: request failed: %v", err)
		} else if code >= 200 && code < 300 {
			r.ok("a right-size upload works (HTTP %d)", code)
		} else {
			r.bad("a right-size upload was refused (HTTP %d)", code)
		}
	}

	fmt.Println("\n4. test files deleted (see any ✗ above)")
	if r.failed {
		fmt.Println("\nR2 check FAILED")
		os.Exit(1)
	}
	fmt.Println("\nR2 check passed")
}
