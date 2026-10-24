package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequireUser(t *testing.T) {
	accounts := map[string]int{"alive": 0} // id -> token version
	var lookups int
	var fail error
	lookup := func(_ context.Context, id string) (int, bool, error) {
		lookups++
		if fail != nil {
			return 0, false, fail
		}
		v, ok := accounts[id]
		return v, ok, nil
	}
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	h := newUserGuard(lookup, func() time.Time { return now }).Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	callV := func(id string, version int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/appliances", nil)
		if id != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &Claims{UserID: id, Version: version}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call := func(id string) *httptest.ResponseRecorder { return callV(id, 0) }

	if got := call("alive").Code; got != http.StatusNoContent {
		t.Fatalf("a user with an account = %d", got)
	}
	// Remembered: no second lookup within the minute.
	call("alive")
	if lookups != 1 {
		t.Errorf("lookups = %d, want 1", lookups)
	}

	// Gone: 401, in words the app can act on.
	w := call("gone")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"account_not_found"`) {
		t.Errorf("a user without an account = %d %s", w.Code, w.Body)
	}

	// Deleted while remembered: noticed once the minute is up.
	delete(accounts, "alive")
	if got := call("alive").Code; got != http.StatusNoContent {
		t.Errorf("still remembered = %d", got)
	}
	now = now.Add(userSeenFor)
	if got := call("alive").Code; got != http.StatusUnauthorized {
		t.Errorf("after the minute = %d, want 401", got)
	}

	// The check failing lets the request through rather than signing out.
	fail = errors.New("db down")
	if got := call("someone").Code; got != http.StatusNoContent {
		t.Errorf("check failed = %d, want the request to go on", got)
	}
	fail = nil

	// No claims (a public route): untouched.
	if got := call("").Code; got != http.StatusNoContent {
		t.Errorf("no claims = %d", got)
	}
}

func TestRequireUserRevokesOldTokenVersions(t *testing.T) {
	accounts := map[string]int{"u": 0}
	lookup := func(_ context.Context, id string) (int, bool, error) {
		v, ok := accounts[id]
		return v, ok, nil
	}
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	h := newUserGuard(lookup, func() time.Time { return now }).Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	call := func(version int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/me", nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &Claims{UserID: "u", Version: version}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if got := call(0).Code; got != http.StatusNoContent {
		t.Fatalf("current version = %d", got)
	}

	// The password changes: version 1. A token from a fresh sign-in (v1) must
	// work at once even though version 0 is still remembered...
	accounts["u"] = 1
	if got := call(1).Code; got != http.StatusNoContent {
		t.Errorf("fresh token after a password change = %d, want 204", got)
	}
	// ...and the old token (v0) is refused straight away: the remembered
	// version is now 1, so it no longer matches.
	w := call(0)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"session_revoked"`) {
		t.Errorf("old token = %d %s, want 401 session_revoked", w.Code, w.Body)
	}
}

func TestForgetEndsTheMinuteOfRemembering(t *testing.T) {
	accounts := map[string]int{"u": 0}
	var lookups int
	lookup := func(_ context.Context, id string) (int, bool, error) {
		lookups++
		v, ok := accounts[id]
		return v, ok, nil
	}
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	g := newUserGuard(lookup, func() time.Time { return now })
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(version int) int {
		r := httptest.NewRequest(http.MethodGet, "/me", nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &Claims{UserID: "u", Version: version}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if got := call(0); got != http.StatusNoContent {
		t.Fatalf("before = %d", got)
	}
	accounts["u"] = 1 // password changed in this process
	if got := call(0); got != http.StatusNoContent {
		t.Fatalf("remembered = %d: the guard should still trust its minute-old answer", got)
	}
	g.Forget("u")
	if got := call(0); got != http.StatusUnauthorized {
		t.Errorf("old token right after Forget = %d, want 401", got)
	}
	if got := call(1); got != http.StatusNoContent {
		t.Errorf("new token after Forget = %d, want 204", got)
	}
}
