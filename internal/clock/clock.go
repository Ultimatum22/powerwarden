// Package clock provides the time source the engine acts on, so tests can
// simulate a week in milliseconds instead of waiting on a real clock.
package clock

import "time"

// Clock is the engine's only source of "now". Implementations must be safe
// for concurrent use.
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// Trusted reports whether Now can be believed. The engine must not act
	// on a clock that isn't trusted yet (see internal/engine).
	Trusted() bool
}

// minTrustedYear is a floor on what "now" can plausibly be: this software
// was not built before this year, so a clock reporting earlier than this
// (e.g. a Pi that booted with no RTC and no NTP sync yet, showing 1970 or
// the build date) is definitely wrong.
const minTrustedYear = 2026

// Real is the system clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) Trusted() bool { return time.Now().Year() >= minTrustedYear }

var _ Clock = Real{}
