package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey int

const userCtxKey ctxKey = iota

func bearerTokenFromHeader(h string) (string, bool) {
	if h == "" {
		return "", false
	}
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

func keyFuncHMAC(secret string) jwt.Keyfunc {
	return func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	}
}

// RequireAuth lets a request through only with a valid access token. A refresh
// token is refused here: it exists to be traded for a new pair at
// /auth/refresh, and accepting it would turn a 24 hour session into 7 days.
func RequireAuth(secret string, newClaims func() *Claims) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			raw, ok := bearerTokenFromHeader(authHeader)
			if !ok {
				log.Printf("[AUTH] ❌ missing bearer token from %s %s", r.Method, r.URL.Path)
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}

			claims, err := parseInto(newClaims(), secret, raw, TypeAccess)
			if err != nil {
				log.Printf("[AUTH] ❌ %v (%s %s)", err, r.Method, r.URL.Path)
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			// Passed — log summary of claims
			exp := claims.ExpiresAt.Time
			expStr := exp.Format(time.RFC3339)
			uid := claims.UserID
			role := claims.Role
			iss := claims.Issuer
			log.Printf("[AUTH] ✅ user=%s role=%s iss=%s exp=%s path=%s",
				uid, role, iss, expStr, r.URL.Path)

			ctx := context.WithValue(r.Context(), userCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OptionalAuth
func OptionalAuth(secret string, newClaims func() *Claims) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			raw, ok := bearerTokenFromHeader(authHeader)
			if ok {
				if claims, err := parseInto(newClaims(), secret, raw, TypeAccess); err == nil {
					log.Printf("[AUTH] ℹ️ optional ok user=%s role=%s path=%s",
						claims.UserID, claims.Role, r.URL.Path)
					r = r.WithContext(context.WithValue(r.Context(), userCtxKey, claims))
				} else {
					log.Printf("[AUTH] ⚠️ optional token invalid: %v path=%s", err, r.URL.Path)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ClaimsFrom(r *http.Request) *Claims {
	if v := r.Context().Value(userCtxKey); v != nil {
		if c, ok := v.(*Claims); ok {
			return c
		}
	}
	return nil
}

func UserIDFrom(r *http.Request) (string, bool) {
	if c := ClaimsFrom(r); c != nil && c.UserID != "" {
		return c.UserID, true
	}
	return "", false
}

// RequireAdmin — auth + log 
func RequireAdmin(secret string, newClaims func() *Claims) func(http.Handler) http.Handler {
	base := RequireAuth(secret, newClaims)
	return func(next http.Handler) http.Handler {
		return base(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c := ClaimsFrom(r); c == nil || c.Role != "admin" {
				log.Printf("[AUTH] 🚫 forbidden non-admin user=%v path=%s", c, r.URL.Path)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}
