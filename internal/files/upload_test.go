package files

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
	"github.com/beamMiter/HomeHQ-server/internal/httpx"
	"github.com/beamMiter/HomeHQ-server/internal/storage"
)

func TestCanonicalMIME(t *testing.T) {
	ok := map[string]string{
		"image/jpeg":                "image/jpeg",
		"IMAGE/JPEG":                "image/jpeg",
		"image/jpg":                 "image/jpeg",
		" image/png ":               "image/png",
		"image/webp; charset=utf-8": "image/webp",
		"image/heic":                "image/heic",
		"application/pdf":           "application/pdf",
		"video/mp4":                 "video/mp4",
	}
	for in, want := range ok {
		if got, good := canonicalMIME(in); !good || got != want {
			t.Errorf("canonicalMIME(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{
		"", "image/svg+xml", "image/*", "image/bmp", "text/html", "application/javascript",
		"application/octet-stream", "video/quicktime", "image", "image/png; =", "../../etc/passwd",
	} {
		if got, good := canonicalMIME(in); good {
			t.Errorf("canonicalMIME(%q) accepted as %q", in, got)
		}
	}
}

func TestContentMatches(t *testing.T) {
	pad := func(s string) []byte { return append([]byte(s), make([]byte, 16)...)[:16] }
	cases := []struct {
		mtype string
		head  []byte
		want  bool
	}{
		{"image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, true},
		{"image/jpeg", pad("<html><script>"), false},
		{"image/png", pad("\x89PNG\r\n\x1a\n"), true},
		{"image/png", pad("<svg xmlns="), false},
		{"image/gif", pad("GIF89a"), true},
		{"image/gif", pad("GIF90a"), false},
		{"image/webp", pad("RIFF\x00\x00\x00\x00WEBP"), true},
		{"image/webp", pad("RIFF\x00\x00\x00\x00WAVE"), false},
		{"image/heic", pad("\x00\x00\x00\x18ftypheic"), true},
		{"image/heif", pad("\x00\x00\x00\x18ftypmif1"), true},
		{"image/heic", pad("\x00\x00\x00\x18ftypisom"), false},
		{"video/mp4", pad("\x00\x00\x00\x18ftypisom"), true},
		{"video/mp4", pad("MZ\x90\x00"), false},
		{"application/pdf", pad("%PDF-1.7"), true},
		{"application/pdf", pad("<html>"), false},
		{"image/png", nil, false},
		{"image/png", []byte{0x89}, false},
		{"text/html", pad("<html>"), false},
	}
	for _, c := range cases {
		if got := contentMatches(c.mtype, c.head); got != c.want {
			t.Errorf("contentMatches(%q, %q) = %v, want %v", c.mtype, c.head, got, c.want)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	cases := []struct{ name, mtype, want string }{
		{"photo.jpg", "image/jpeg", "photo.jpg"},
		{"IMG 0042.JPEG", "image/jpeg", "IMG 0042.jpg"},
		{"bill.php", "application/pdf", "bill.pdf"},                // extension comes from the verified type
		{"../../etc/passwd", "image/png", "passwd.png"},            // no path
		{`C:\Users\me\receipt.png`, "image/png", "receipt.png"},    // Windows path
		{"a\x00b\nc<script>.png", "image/png", "a_b_c_script.png"}, // control and markup characters
		{"ใบเสร็จ.png", "image/png", "ใบเสร็จ.png"},                // Thai is kept
		{"", "image/png", "file.png"},
		{"...", "application/pdf", "file.pdf"},
		{strings.Repeat("x", 500) + ".png", "image/png", strings.Repeat("x", 80) + ".png"},
	}
	for _, c := range cases {
		if got := safeFilename(c.name, c.mtype); got != c.want {
			t.Errorf("safeFilename(%q, %q) = %q, want %q", c.name, c.mtype, got, c.want)
		}
	}
}

// ── the handler, as far as it can go without a database ─────────────────────

type fakeStorage struct{ saved, presigned int }

func (f *fakeStorage) Save(_ context.Context, _ string, r io.Reader, _, _ string, _ int64) (storage.PutResult, error) {
	f.saved++
	_, _ = io.Copy(io.Discard, r)
	return storage.PutResult{}, nil
}
func (f *fakeStorage) PresignPut(_ context.Context, _, _, _ string, _ int64) (storage.Presign, error) {
	f.presigned++
	return storage.Presign{URL: "https://example.test/put"}, nil
}
func (f *fakeStorage) Delete(context.Context, string) error { return nil }

const secret = "files-test-secret-files-test-secret"

func rig(t *testing.T) (*fakeStorage, http.Handler, string) {
	t.Helper()
	st := &fakeStorage{}
	h := Handler{Storage: st} // no Repo: every case here is refused before the database
	mux := chi.NewRouter()
	mux.Use(httpx.BodyLimit(httpx.DefaultMaxBody, httpx.BodyRule{Path: "/uploads", Limit: MaxUploadBody}))
	mux.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(secret, auth.NewClaims))
		h.RegisterRoutes(pr)
	})
	tok, err := auth.SignJWTWithRole(secret, "user-1", "u@example.test", "member", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return st, mux, tok
}

func multipartBody(t *testing.T, field, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	hdr.Set("Content-Type", contentType)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func upload(t *testing.T, h http.Handler, tok, filename, contentType string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, "file", filename, contentType, content)
	r := httptest.NewRequest(http.MethodPost, "/uploads", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUploadRefusesWhatItShould(t *testing.T) {
	st, h, tok := rig(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

	cases := []struct {
		name, filename, ctype string
		content               []byte
		want                  int
	}{
		{"svg", "x.svg", "image/svg+xml", []byte("<svg onload=alert(1)>"), 400},
		{"html claiming to be a png", "x.png", "image/png", []byte("<html><script>alert(1)</script></html>"), 400},
		{"executable claiming to be a pdf", "x.pdf", "application/pdf", []byte("MZ\x90\x00\x03"), 400},
		{"unlisted image type", "x.bmp", "image/bmp", []byte("BM\x00\x00"), 400},
		{"no declared type, unknown extension", "x.bin", "", png, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := upload(t, h, tok, c.filename, c.ctype, c.content)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
		})
	}
	if st.saved != 0 {
		t.Errorf("%d refused uploads still reached storage", st.saved)
	}
}

func TestUploadNeedsAToken(t *testing.T) {
	_, h, _ := rig(t)
	if w := upload(t, h, "", "x.png", "image/png", []byte("x")); w.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", w.Code)
	}
}

func TestUploadBodyIsCapped(t *testing.T) {
	st, h, tok := rig(t)
	// Over the whole-request ceiling: refused before any parsing.
	big := bytes.Repeat([]byte("a"), int(MaxUploadBody)+1)
	body, ct := multipartBody(t, "file", "x.png", "image/png", big)
	r := httptest.NewRequest(http.MethodPost, "/uploads", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize upload = %d, want 413", w.Code)
	}
	if st.saved != 0 {
		t.Error("an oversize upload reached storage")
	}
}

func TestPresignValidatesBeforeSigning(t *testing.T) {
	st, h, tok := rig(t)
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/uploads/presign", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for name, body := range map[string]string{
		"svg":        `{"filename":"a.svg","mime":"image/svg+xml","size":100}`,
		"html":       `{"filename":"a.html","mime":"text/html","size":100}`,
		"too big":    `{"filename":"a.png","mime":"image/png","size":26214401}`,
		"no size":    `{"filename":"a.png","mime":"image/png","size":0}`,
		"negative":   `{"filename":"a.png","mime":"image/png","size":-5}`,
		"no mime":    `{"filename":"a.png","size":100}`,
		"bad json":   `{`,
		"unexpected": ``,
	} {
		if got := post(body); got != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, got)
		}
	}
	if st.presigned != 0 {
		t.Errorf("%d refused requests were still signed", st.presigned)
	}
	if got := post(`{"filename":"a.png","mime":"image/png","size":1000}`); got != http.StatusOK {
		t.Errorf("a valid request = %d, want 200", got)
	}
}
