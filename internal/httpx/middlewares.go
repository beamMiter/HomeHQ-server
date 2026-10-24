package httpx

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// SecurityHeaders: เสริมความปลอดภัยฝั่งบราวเซอร์
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Security / clickjacking / MIME
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		// Referrer & Permissions
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// Cross-origin isolation/resource policy (เหมาะกับ API)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")

		// HSTS (เปิดเมื่ออยู่หลัง HTTPS เท่านั้น) — TLS usually ends at the
		// proxy, which says so in X-Forwarded-Proto. Browsers ignore the header
		// over plain http, so a forged value gains a caller nothing.
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}

		// CSP – สำหรับ API ให้ปิดทุกอย่าง
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		next.ServeHTTP(w, r)
	})
}

// NoCache: ปิด cache สำหรับทุก response API
func NoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// Options configures CommonMiddlewares.
type Options struct {
	// CorsOrigin is the one browser origin allowed ("" = none, "*" = any).
	CorsOrigin string
	// Development also allows localhost origins, for a web build run locally.
	Development bool
	// TrustedProxyHops is how many proxies in front of us append to
	// X-Forwarded-For (see ClientIP).
	TrustedProxyHops int
	// MaxBody caps request bodies (0 = DefaultMaxBody); BodyRules raise or
	// lower it for single paths.
	MaxBody   int64
	BodyRules []BodyRule
}

func CommonMiddlewares(o Options) []func(http.Handler) http.Handler {
	maxBody := o.MaxBody
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}

	mws := []func(http.Handler) http.Handler{
		middleware.RequestID,
		RealClientIP(o.TrustedProxyHops),
		middleware.Recoverer,
		middleware.Timeout(60 * time.Second),
		BodyLimit(maxBody, o.BodyRules...),

		middleware.AllowContentType("application/json", "multipart/form-data"),

		middleware.Compress(5),
		middleware.Logger,
		SecurityHeaders,
		NoCache,
	}

	// The app is a native client and authenticates with a bearer header, so no
	// cookies are involved (no AllowCredentials). With no origin to allow, the
	// handler is left out: go-chi/cors treats an empty list as "allow all".
	var origins []string
	if o.CorsOrigin != "" {
		origins = append(origins, o.CorsOrigin)
	}
	if o.Development {
		origins = append(origins, "http://localhost:*", "http://127.0.0.1:*")
	}
	if len(origins) > 0 {
		mws = append(mws,
			cors.Handler(cors.Options{
				AllowedOrigins: origins,
				AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"},
				AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
				ExposedHeaders: []string{"Link"},
				MaxAge:         300,
			}),
			func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Add("Vary", "Origin")
					next.ServeHTTP(w, r)
				})
			},
		)
	}
	return mws
}
