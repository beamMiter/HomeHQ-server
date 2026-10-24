package auth

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// UserLookup reports whether a user id still has an account and, if so, the
// token version stored for it.
type UserLookup func(ctx context.Context, id string) (version int, found bool, err error)

// userSeenFor is how long a user found once is taken as still there.
const userSeenFor = time.Minute

type seenUser struct {
	at      time.Time
	version int
}

// UserGuard is the check behind RequireUser, with a handle for ending a user's
// remembered state at once (Forget).
type UserGuard struct {
	lookup UserLookup
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]seenUser
}

// NewUserGuard returns a guard that asks lookup about users it has not seen
// within the last minute.
func NewUserGuard(lookup UserLookup) *UserGuard {
	return newUserGuard(lookup, time.Now)
}

func newUserGuard(lookup UserLookup, now func() time.Time) *UserGuard {
	return &UserGuard{lookup: lookup, now: now, seen: map[string]seenUser{}}
}

// Forget drops what is remembered about a user, so the very next request asks
// the database. Call it when the user's token version has just been raised in
// this process (password changed or reset): without it a token that was
// accepted a moment ago would still pass for up to a minute.
func (g *UserGuard) Forget(id string) {
	g.mu.Lock()
	delete(g.seen, id)
	g.mu.Unlock()
}

// RequireUser goes after RequireAuth. It does two checks a signed token cannot
// do for itself:
//
//   - The account exists. A token stays valid after its user is gone (the
//     account was deleted, or the database was reset under a running app);
//     without this check such a request goes on and fails later on a foreign
//     key, as a confusing 5xx. Instead it answers 401
//     {"error":"account_not_found"}, which sends the app back to sign in.
//   - The token is not revoked. Changing or resetting a password raises the
//     user's token version, and a token carrying an older one answers 401
//     {"error":"session_revoked"}.
//
// A user found is remembered for a minute, so this is not a query per request
// (a revocation made by another process takes up to a minute to bite on a
// token already accepted; one made here is immediate, through Forget). If the
// check itself fails (the database is down) the request goes on: it would fail
// on its own, and a hiccup must not sign everyone out.
func RequireUser(lookup UserLookup) func(http.Handler) http.Handler {
	return NewUserGuard(lookup).Middleware
}

func deny(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
}

// Middleware is the http middleware form of the guard.
func (g *UserGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := ClaimsFrom(r)
		if c == nil || c.UserID == "" {
			next.ServeHTTP(w, r)
			return
		}
		t := g.now()
		g.mu.Lock()
		s, ok := g.seen[c.UserID]
		g.mu.Unlock()
		// A remembered user is trusted only while the token's version is the
		// one remembered; a different one may be a fresh sign-in after a
		// password change, so it goes to the database to be sure.
		if ok && t.Sub(s.at) < userSeenFor && s.version == c.Version {
			next.ServeHTTP(w, r)
			return
		}

		version, found, err := g.lookup(r.Context(), c.UserID)
		if err != nil {
			log.Printf("[AUTH] ⚠️ could not check user %s: %v", c.UserID, err)
			next.ServeHTTP(w, r)
			return
		}
		if !found {
			log.Printf("[AUTH] ❌ user %s has no account (%s %s)", c.UserID, r.Method, r.URL.Path)
			g.Forget(c.UserID)
			deny(w, "account_not_found")
			return
		}

		g.mu.Lock()
		g.seen[c.UserID] = seenUser{at: t, version: version}
		// A household has a handful of users; forget stale ones now and then
		// so the map can't grow without end.
		if len(g.seen) > 1000 {
			for id, s := range g.seen {
				if t.Sub(s.at) >= userSeenFor {
					delete(g.seen, id)
				}
			}
		}
		g.mu.Unlock()

		if version != c.Version {
			log.Printf("[AUTH] ❌ user %s token version %d, current %d (%s %s)", c.UserID, c.Version, version, r.Method, r.URL.Path)
			deny(w, "session_revoked")
			return
		}
		next.ServeHTTP(w, r)
	})
}
