package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reqFrom(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name string
		r    *http.Request
		hops int
		want string
	}{
		{"no proxy ignores the header", reqFrom("203.0.113.9:5000", "1.2.3.4"), 0, "203.0.113.9"},
		{"one proxy: last entry", reqFrom("10.0.0.1:80", "198.51.100.7"), 1, "198.51.100.7"},
		{"a forged first entry is not believed", reqFrom("10.0.0.1:80", "6.6.6.6, 198.51.100.7"), 1, "198.51.100.7"},
		{"two proxies: second from the right", reqFrom("10.0.0.1:80", "6.6.6.6, 198.51.100.7, 10.0.0.2"), 2, "198.51.100.7"},
		{"header split over lines", reqFrom("10.0.0.1:80", "6.6.6.6", "198.51.100.7"), 1, "198.51.100.7"},
		{"fewer entries than hops falls back", reqFrom("203.0.113.9:5000", "198.51.100.7"), 2, "203.0.113.9"},
		{"garbage entry falls back", reqFrom("203.0.113.9:5000", "not-an-ip"), 1, "203.0.113.9"},
		{"no header falls back", reqFrom("203.0.113.9:5000"), 1, "203.0.113.9"},
		{"ipv6 is normalised", reqFrom("[::1]:80", "2001:DB8::1"), 1, "2001:db8::1"},
		{"address without a port", reqFrom("203.0.113.9"), 0, "203.0.113.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClientIP(c.r, c.hops); got != c.want {
				t.Errorf("ClientIP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRealClientIPRewritesRemoteAddr(t *testing.T) {
	var seen string
	h := RealClientIP(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = RemoteIP(r) }))
	h.ServeHTTP(httptest.NewRecorder(), reqFrom("10.0.0.1:80", "6.6.6.6, 198.51.100.7"))
	if seen != "198.51.100.7" {
		t.Errorf("handler saw %q, want 198.51.100.7", seen)
	}
}

func TestBodyLimit(t *testing.T) {
	read := func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	h := BodyLimit(10, BodyRule{Path: "/big", Limit: 100})(http.HandlerFunc(read))

	do := func(path, body string, declared bool) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if !declared {
			r.ContentLength = -1
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if got := do("/x", "0123456789", true); got != http.StatusOK {
		t.Errorf("body at the limit = %d, want 200", got)
	}
	if got := do("/x", "0123456789A", true); got != http.StatusRequestEntityTooLarge {
		t.Errorf("declared oversize body = %d, want 413", got)
	}
	if got := do("/x", "0123456789A", false); got != http.StatusRequestEntityTooLarge {
		t.Errorf("undeclared oversize body = %d, want 413 while reading", got)
	}
	if got := do("/big", strings.Repeat("a", 100), true); got != http.StatusOK {
		t.Errorf("rule path within its limit = %d, want 200", got)
	}
	if got := do("/big", strings.Repeat("a", 101), true); got != http.StatusRequestEntityTooLarge {
		t.Errorf("rule path over its limit = %d, want 413", got)
	}
}

func TestLimiterRefillsAndBounds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newLimiter(60, 3, func() time.Time { return now }) // 1 token a second, burst 3

	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("call %d refused inside the burst", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th call allowed, burst is 3")
	}
	if !l.Allow("b") {
		t.Fatal("another key shares the first key's bucket")
	}
	if d := l.RetryAfter("a"); d <= 0 || d > time.Second {
		t.Errorf("RetryAfter = %v, want within (0, 1s]", d)
	}
	now = now.Add(2 * time.Second)
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("two tokens should have come back after 2s")
	}
	if l.Allow("a") {
		t.Fatal("only two tokens should have come back")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatal("bucket should be full again, capped at the burst")
		}
	}
	if l.Allow("a") {
		t.Fatal("refill must not exceed the burst")
	}
}

func TestLimiterPeekTakeForget(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newLimiter(1, 2, func() time.Time { return now })

	if !l.Peek("k") || !l.Peek("k") {
		t.Fatal("Peek must not use tokens up")
	}
	l.Take("k")
	l.Take("k")
	if l.Peek("k") {
		t.Fatal("two Takes should have emptied a bucket of 2")
	}
	l.Take("k") // below empty stays at empty, never negative
	now = now.Add(time.Minute)
	if !l.Peek("k") {
		t.Fatal("a minute at 1/min should give a token back")
	}
	l.Take("k")
	l.Forget("k")
	if !l.Peek("k") {
		t.Fatal("Forget should give the key a full bucket again")
	}
}

func TestLimiterKeyCountIsBounded(t *testing.T) {
	l := NewLimiter(1, 1)
	for i := 0; i < maxLimiterKeys*2; i++ {
		l.Take("key-" + strconv.Itoa(i))
	}
	l.mu.Lock()
	n := len(l.keys)
	l.mu.Unlock()
	if n > maxLimiterKeys {
		t.Errorf("limiter holds %d keys, cap is %d", n, maxLimiterKeys)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	l := NewLimiter(1, 2)
	h := RateLimit(l, RemoteIP)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(ip string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, reqFrom(ip+":1"))
		return w
	}
	for i := 0; i < 2; i++ {
		if w := call("1.1.1.1"); w.Code != http.StatusNoContent {
			t.Fatalf("call %d = %d", i+1, w.Code)
		}
	}
	w := call("1.1.1.1")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd call = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 without a Retry-After header")
	}
	if w := call("2.2.2.2"); w.Code != http.StatusNoContent {
		t.Errorf("a different address was limited: %d", w.Code)
	}
}

func TestEscapeLikeAndLikeContains(t *testing.T) {
	cases := map[string]string{
		"plain":  "plain",
		"100%":   `100\%`,
		"a_b":    `a\_b`,
		`a\b`:    `a\\b`,
		`%_\`:    `\%\_\\`,
		"ไทย%":   `ไทย\%`,
		"":       "",
		"%%%%__": `\%\%\%\%\_\_`,
	}
	for in, want := range cases {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}

	if got := LikeContains("  50% off "); got != `%50\% off%` {
		t.Errorf("LikeContains trims and escapes: got %q", got)
	}
	if got := LikeContains("a\x00b"); got != "%ab%" {
		t.Errorf("LikeContains keeps a NUL byte: %q", got)
	}
	long := strings.Repeat("ก", MaxSearchLen+50)
	got := LikeContains(long)
	if n := len([]rune(got)); n != MaxSearchLen+2 {
		t.Errorf("LikeContains kept %d characters, want %d", n-2, MaxSearchLen)
	}
}

func TestCommonMiddlewaresCors(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	build := func(o Options) http.Handler {
		var h http.Handler = ok
		mws := CommonMiddlewares(o)
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
	get := func(h http.Handler, origin string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header().Get("Access-Control-Allow-Origin")
	}

	// Production default: no origin configured, no CORS headers at all.
	if got := get(build(Options{}), "https://evil.example"); got != "" {
		t.Errorf("empty config allowed origin %q", got)
	}
	// Localhost is only for development.
	if got := get(build(Options{}), "http://localhost:3000"); got != "" {
		t.Errorf("localhost allowed outside development: %q", got)
	}
	if got := get(build(Options{Development: true}), "http://localhost:3000"); got == "" {
		t.Error("localhost refused in development")
	}
	// A named origin is allowed, a different one is not.
	named := build(Options{CorsOrigin: "https://app.example"})
	if got := get(named, "https://app.example"); got != "https://app.example" {
		t.Errorf("named origin = %q", got)
	}
	if got := get(named, "https://evil.example"); got != "" {
		t.Errorf("other origin allowed: %q", got)
	}
}
