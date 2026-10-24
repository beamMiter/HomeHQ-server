package httpx

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// maxLimiterKeys bounds the memory a flood of distinct keys (spoofed
// addresses, made-up emails) can take.
const maxLimiterKeys = 10000

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a token bucket per key, kept in memory. It protects one process:
// run a single instance, or put a shared limiter in front, for it to count
// everything.
type Limiter struct {
	mu    sync.Mutex
	rate  float64 // tokens added per second
	burst float64
	now   func() time.Time
	keys  map[string]*bucket
}

// NewLimiter allows burst calls at once and perMinute calls a minute after.
func NewLimiter(perMinute float64, burst int) *Limiter {
	return newLimiter(perMinute, burst, time.Now)
}

func newLimiter(perMinute float64, burst int, now func() time.Time) *Limiter {
	return &Limiter{
		rate:  perMinute / 60,
		burst: float64(burst),
		now:   now,
		keys:  map[string]*bucket{},
	}
}

// refilled returns key's bucket brought up to date; the caller holds l.mu.
func (l *Limiter) refilled(key string) *bucket {
	t := l.now()
	b, ok := l.keys[key]
	if !ok {
		if len(l.keys) >= maxLimiterKeys {
			l.prune(t)
		}
		b = &bucket{tokens: l.burst, last: t}
		l.keys[key] = b
		return b
	}
	b.tokens = math.Min(l.burst, b.tokens+t.Sub(b.last).Seconds()*l.rate)
	b.last = t
	return b
}

// prune forgets buckets that have refilled completely (they carry no
// information), then, if the map is still full, arbitrary ones.
func (l *Limiter) prune(t time.Time) {
	for k, b := range l.keys {
		if b.tokens+t.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.keys, k)
		}
	}
	for k := range l.keys {
		if len(l.keys) < maxLimiterKeys {
			return
		}
		delete(l.keys, k)
	}
}

// Allow takes one token for key and reports whether there was one.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refilled(key)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Peek reports whether Allow would succeed, without taking a token. Use it
// with Take to count only the attempts that failed.
func (l *Limiter) Peek(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refilled(key).tokens >= 1
}

// Take spends one token for key, down to empty.
func (l *Limiter) Take(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refilled(key)
	b.tokens = math.Max(0, b.tokens-1)
}

// Forget drops key, as if it had never been seen (after a good sign-in).
func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

// RetryAfter is how long until key has a token again.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refilled(key)
	if b.tokens >= 1 || l.rate <= 0 {
		return 0
	}
	return time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// WriteRateLimited answers 429 with a Retry-After header.
func WriteRateLimited(w http.ResponseWriter, retry time.Duration) {
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	JSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
}

// RateLimit takes one token per request from l, keyed by key(r), and answers
// 429 when there is none.
func RateLimit(l *Limiter, key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if !l.Allow(k) {
				WriteRateLimited(w, l.RetryAfter(k))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
