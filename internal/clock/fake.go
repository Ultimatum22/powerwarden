package clock

import (
	"sync"
	"time"
)

// Fake is a settable Clock for tests, letting engine tests simulate a week
// of ticks without waiting on real time.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	trusted bool
}

// NewFake builds a Fake starting at t, trusted by default.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t, trusted: true}
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Trusted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trusted
}

// Set moves the clock to t (which may be before or after the current
// value; callers driving a simulation normally only move it forward).
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the clock forward by d and returns the new time.
func (f *Fake) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// SetTrusted controls what Trusted reports, for testing the "clock isn't
// trustworthy yet" case.
func (f *Fake) SetTrusted(trusted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trusted = trusted
}

var _ Clock = (*Fake)(nil)
