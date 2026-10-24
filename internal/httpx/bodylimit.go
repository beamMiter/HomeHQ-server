package httpx

import "net/http"

// DefaultMaxBody is the request body ceiling for ordinary JSON calls.
const DefaultMaxBody int64 = 1 << 20

// BodyRule gives one path its own request body ceiling (an upload, say).
type BodyRule struct {
	Path  string
	Limit int64
}

// BodyLimit caps request bodies at def bytes, or at a rule's limit for the
// paths it names. A declared Content-Length over the cap is refused with 413
// before anything is read; a body that turns out larger (no length, or a lie)
// is cut off while being read.
func BodyLimit(def int64, rules ...BodyRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := def
			for _, rule := range rules {
				if r.URL.Path == rule.Path {
					limit = rule.Limit
					break
				}
			}
			if r.ContentLength > limit {
				JSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
