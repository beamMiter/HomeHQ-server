package user

import (
	"time"

	"github.com/beamMiter/HomeHQ-server/internal/httpx"
)

// Throttle slows down password guessing. Only failures count, so signing in
// normally is never limited, and it works on two keys: the address the guesses
// come from, and the account they aim at (which also stops a guesser who
// changes address). Both are in memory, so run one instance.
//
// The cost of limiting by account is that someone who knows an email can lock
// that account's sign-in for a few minutes. For a household that is the better
// side of the trade: they still cannot get in, and the real owner waits.
//
// A nil *Throttle limits nothing.
type Throttle struct {
	ip    *httpx.Limiter
	email *httpx.Limiter
}

// Failures allowed before the first refusal, and how often one more is allowed
// after that: 10 per account then one per 90 s (about 40 an hour), 20 per
// address then one per 45 s.
const (
	emailBurst    = 10
	emailRefill   = 90 * time.Second
	addressBurst  = 20
	addressRefill = 45 * time.Second
)

func perMinute(every time.Duration) float64 { return float64(time.Minute) / float64(every) }

func NewThrottle() *Throttle {
	return &Throttle{
		email: httpx.NewLimiter(perMinute(emailRefill), emailBurst),
		ip:    httpx.NewLimiter(perMinute(addressRefill), addressBurst),
	}
}

// Blocked reports whether another attempt is refused, and for how long.
func (t *Throttle) Blocked(ip, email string) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	var wait time.Duration
	if !t.ip.Peek(ip) {
		wait = t.ip.RetryAfter(ip)
	}
	if email != "" && !t.email.Peek(email) {
		if w := t.email.RetryAfter(email); w > wait {
			wait = w
		}
	}
	return wait, wait > 0
}

// Fail records a wrong password or unknown account.
func (t *Throttle) Fail(ip, email string) {
	if t == nil {
		return
	}
	t.ip.Take(ip)
	if email != "" {
		t.email.Take(email)
	}
}

// Succeed clears the account's failures after a good sign-in. The address's
// are kept: a guesser must not wipe them by signing in to an account of their own.
func (t *Throttle) Succeed(email string) {
	if t == nil || email == "" {
		return
	}
	t.email.Forget(email)
}
