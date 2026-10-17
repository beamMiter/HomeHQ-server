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
	accounts := map[string]bool{"alive": true}
	var lookups int
	var fail error
	exists := func(_ context.Context, id string) (bool, error) {
		lookups++
		if fail != nil {
			return false, fail
		}
		return accounts[id], nil
	}
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	h := requireUser(exists, func() time.Time { return now })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	call := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/appliances", nil)
		if id != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &Claims{UserID: id}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

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

	// No claims (a public route): untouched.
	if got := call("").Code; got != http.StatusNoContent {
		t.Errorf("no claims = %d", got)
	}
}
