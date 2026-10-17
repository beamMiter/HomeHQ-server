package auth

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// UserExists reports whether a user id still has an account.
type UserExists func(ctx context.Context, id string) (bool, error)

// userSeenFor is how long a user found once is taken as still there.
const userSeenFor = time.Minute

// RequireUser goes after RequireAuth. A token stays valid after its user is
// gone (the account was deleted, or the database was reset under a running
// app); without this check such a request goes on and fails later on a
// foreign key, as a confusing 5xx. Instead it answers 401
// {"error":"account_not_found"}, which sends the app back to sign in.
//
// A user found is remembered for a minute, so this is not a query per
// request. If the check itself fails (the database is down) the request goes
// on: it would fail on its own, and a hiccup must not sign everyone out.
func RequireUser(exists UserExists) func(http.Handler) http.Handler {
	return requireUser(exists, time.Now)
}

func requireUser(exists UserExists, now func() time.Time) func(http.Handler) http.Handler {
	var (
		mu   sync.Mutex
		seen = map[string]time.Time{}
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := ClaimsFrom(r)
			if c == nil || c.UserID == "" {
				next.ServeHTTP(w, r)
				return
			}
			t := now()
			mu.Lock()
			at, ok := seen[c.UserID]
			mu.Unlock()
			if ok && t.Sub(at) < userSeenFor {
				next.ServeHTTP(w, r)
				return
			}

			found, err := exists(r.Context(), c.UserID)
			if err != nil {
				log.Printf("[AUTH] ⚠️ could not check user %s: %v", c.UserID, err)
				next.ServeHTTP(w, r)
				return
			}
			if !found {
				log.Printf("[AUTH] ❌ user %s has no account (%s %s)", c.UserID, r.Method, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"account_not_found"}`))
				return
			}

			mu.Lock()
			seen[c.UserID] = t
			// A household has a handful of users; forget stale ones now and
			// then so the map can't grow without end.
			if len(seen) > 1000 {
				for id, at := range seen {
					if t.Sub(at) >= userSeenFor {
						delete(seen, id)
					}
				}
			}
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}
