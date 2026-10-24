package files

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/beamMiter/HomeHQ-server/internal/auth"
	"github.com/beamMiter/HomeHQ-server/internal/httpx"
	"github.com/beamMiter/HomeHQ-server/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	// MaxUploadSize is the largest file accepted.
	MaxUploadSize = 25 << 20
	// MaxUploadBody is the ceiling for an upload request: the file plus the
	// multipart framing around it. cmd/api gives /uploads this limit, and the
	// default (small) one to everything else.
	MaxUploadBody = MaxUploadSize + 1<<20
	// multipartMemory is how much of a form is held in memory; the rest of a
	// large file goes to a temporary file.
	multipartMemory = 8 << 20
)

type Handler struct {
	Repo      Repo
	Storage   storage.Service
	JWTSecret string
}

func (h Handler) RegisterRoutes(r chi.Router) {
	r.Post("/uploads", h.upload)
	r.Post("/uploads/presign", h.presign)
	r.Get("/files/{id}", h.getFile)
	r.Delete("/files/{id}", h.deleteFile)
}

func (h Handler) upload(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserIDFrom(r)
	if !ok {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll() //nolint:errcheck // temp files; nothing to do on failure
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "missing file"})
		return
	}
	defer file.Close()

	declared := header.Header.Get("Content-Type")
	if declared == "" {
		declared = mime.TypeByExtension(filepath.Ext(header.Filename))
	}
	mtype, ok := canonicalMIME(declared)
	if !ok {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported file type"})
		return
	}
	if header.Size > MaxUploadSize {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "file too large (max 25MB)"})
		return
	}

	// The type the client claims is only a claim: look at the bytes. Otherwise
	// an HTML or SVG page uploaded as "image/png" would be served back to
	// people's browsers from our own storage address.
	head := make([]byte, 16)
	n, _ := io.ReadFull(file, head)
	if !contentMatches(mtype, head[:n]) {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "file content does not match its type"})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		httpx.Internal(w, r, err)
		return
	}
	filename := safeFilename(header.Filename, mtype)

	result, err := h.Storage.Save(r.Context(), uid, file, filename, mtype, header.Size)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}

	rec := &File{
		OwnerID:  uid,
		Filename: result.Filename,
		MIME:     mtype,
		Size:     result.Size,
		URL:      result.URL,
	}
	if err := h.Repo.Create(r.Context(), rec); err != nil {
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, rec)
}

func (h Handler) presign(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserIDFrom(r)
	if !ok {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var p struct {
		Filename string `json:"filename"`
		MIME     string `json:"mime"`
		Size     int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if p.Filename == "" || p.MIME == "" || p.Size <= 0 {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "filename, mime, size are required"})
		return
	}
	mtype, ok := canonicalMIME(p.MIME)
	if !ok {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported file type"})
		return
	}
	if p.Size > MaxUploadSize {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "file too large (max 25MB)"})
		return
	}

	presign, err := h.Storage.PresignPut(r.Context(), uid, safeFilename(p.Filename, mtype), mtype, p.Size)
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			httpx.JSON(w, http.StatusNotImplemented, map[string]string{"error": "presign not supported"})
			return
		}
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, presign)
}

func (h Handler) getFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := h.Repo.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

func (h Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserIDFrom(r)
	if !ok {
		httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Repo.Delete(r.Context(), uid, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		httpx.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// extensions maps each accepted type to the extension its files get. Anything
// not listed is refused: "image/*" used to pass, which let SVG (a page that
// can run script) and other oddities in.
var extensions = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/heic":      ".heic",
	"image/heif":      ".heif",
	"video/mp4":       ".mp4",
	"application/pdf": ".pdf",
}

// canonicalMIME reduces what a client sent ("IMAGE/JPG; charset=x") to one of
// the accepted types, or reports that it is not one.
func canonicalMIME(m string) (string, bool) {
	base, _, err := mime.ParseMediaType(strings.TrimSpace(m))
	if err != nil {
		return "", false
	}
	if base == "image/jpg" || base == "image/pjpeg" {
		base = "image/jpeg"
	}
	_, ok := extensions[base]
	return base, ok
}

// contentMatches reports whether the first bytes of a file are those of the
// type it claims to be.
func contentMatches(mtype string, head []byte) bool {
	has := func(off int, s string) bool {
		return len(head) >= off+len(s) && string(head[off:off+len(s)]) == s
	}
	switch mtype {
	case "image/jpeg":
		return len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF
	case "image/png":
		return has(0, "\x89PNG\r\n\x1a\n")
	case "image/gif":
		return has(0, "GIF87a") || has(0, "GIF89a")
	case "image/webp":
		return has(0, "RIFF") && has(8, "WEBP")
	case "image/heic", "image/heif":
		if !has(4, "ftyp") || len(head) < 12 {
			return false
		}
		switch string(head[8:12]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "heif":
			return true
		}
		return false
	case "video/mp4":
		return has(4, "ftyp")
	case "application/pdf":
		return has(0, "%PDF-")
	}
	return false
}

// safeFilename keeps what is useful of a client's file name (so the owner
// recognises it) but nothing that can do harm: no path, a short name from a
// plain character set, and the extension of the verified type, not the
// client's.
func safeFilename(name, mtype string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	var b strings.Builder
	for _, r := range stem {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		case r > 0x7F: // letters of other scripts (Thai names) are fine
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	stem = strings.Trim(b.String(), " ._")
	if stem == "" {
		stem = "file"
	}
	return stem + extensions[mtype]
}
