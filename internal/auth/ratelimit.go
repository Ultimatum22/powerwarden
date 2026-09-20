package auth

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter enforces per-key limits with exponential backoff after
// repeated failures, per CLAUDE.md: "Rate limiting: per IP and per account
// on login and step-up, with exponential backoff and lockout". Keys are
// typically "ip:1.2.3.4" or "user:dave"; callers combine both.
//
// Two layers: a steady-state token bucket (golang.org/x/time/rate) caps
// the normal attempt rate, and a separate failure counter imposes a
// growing lockout on top once attempts start failing, so a burst of wrong
// passkey/TOTP attempts gets progressively slower rather than just capped
// at a fixed rate.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry

	// Rate and Burst configure the steady-state token bucket per key.
	Rate  rate.Limit
	Burst int

	// LockoutBase is the wait imposed after the first failure; each
	// subsequent consecutive failure doubles it, up to LockoutMax.
	LockoutBase time.Duration
	LockoutMax  time.Duration
}

type limiterEntry struct {
	bucket    *rate.Limiter
	failures  int
	lockedTil time.Time
	lastUsed  time.Time
}

// NewRateLimiter builds a RateLimiter.
func NewRateLimiter(r rate.Limit, burst int, lockoutBase, lockoutMax time.Duration) *RateLimiter {
	return &RateLimiter{
		entries:     make(map[string]*limiterEntry),
		Rate:        r,
		Burst:       burst,
		LockoutBase: lockoutBase,
		LockoutMax:  lockoutMax,
	}
}

// Allow reports whether an attempt for key is currently permitted: it must
// be outside any active lockout and within the steady-state rate.
func (r *RateLimiter) Allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(key)
	e.lastUsed = now
	if now.Before(e.lockedTil) {
		return false
	}
	return e.bucket.AllowN(now, 1)
}

// Failure records a failed attempt, extending the lockout exponentially.
func (r *RateLimiter) Failure(key string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(key)
	e.failures++
	wait := r.LockoutBase << uint(min(e.failures-1, 20)) // cap the shift to avoid overflow
	if wait > r.LockoutMax || wait <= 0 {
		wait = r.LockoutMax
	}
	e.lockedTil = now.Add(wait)
	e.lastUsed = now
}

// Success clears a key's failure count and lockout (a successful login/
// step-up).
func (r *RateLimiter) Success(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

func (r *RateLimiter) entry(key string) *limiterEntry {
	e, ok := r.entries[key]
	if !ok {
		e = &limiterEntry{bucket: rate.NewLimiter(r.Rate, r.Burst)}
		r.entries[key] = e
	}
	return e
}

// Prune removes entries idle longer than idleAfter, so a long-running
// process doesn't accumulate unbounded memory for one-off IPs.
func (r *RateLimiter) Prune(now time.Time, idleAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.entries {
		if now.Sub(e.lastUsed) > idleAfter {
			delete(r.entries, k)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
