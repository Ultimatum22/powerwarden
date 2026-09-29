// Package clock provides the time source the engine acts on, so tests can
// simulate a week in milliseconds instead of waiting on a real clock.
package clock

import (
	"os"
	"strings"
	"time"
)

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

// Default evidence files for Real.Trusted. Both are plain file reads, so
// they work under the systemd unit's SystemCallFilter=@system-service,
// which does not allow adjtimex (it's in @clock).
const (
	// DefaultSyncMarker is created by systemd-timesyncd once it has
	// synchronised with an NTP server. /run is a tmpfs, so it never
	// survives a reboot.
	DefaultSyncMarker = "/run/systemd/timesync/synchronized"
	// DefaultRTCHCToSys is "1" when the kernel set the system clock from
	// this RTC at boot (the DS3231 via dtoverlay=i2c-rtc,ds3231).
	// fake-hwclock, which restores the last saved time after a power cut,
	// does not set it.
	DefaultRTCHCToSys = "/sys/class/rtc/rtc0/hctosys"
)

// Real is the system clock. The zero value checks the default evidence
// files; tests point the fields elsewhere.
type Real struct {
	SyncMarker string
	RTCHCToSys string
	// AnyPlausible skips the sync/RTC evidence and trusts any time past
	// the build-year floor (config clock.trust: system; development only).
	AnyPlausible bool
}

func (Real) Now() time.Time { return time.Now() }

// Trusted reports whether the system clock is believable: past the
// build-year floor, and either NTP-synchronised or set from a real RTC at
// boot (CLAUDE.md: "NTP synced or RTC present"). A clock that merely looks
// plausible, like one fake-hwclock restored after a power cut, is not
// enough: acting on it would apply the wrong schedule boundaries.
func (r Real) Trusted() bool {
	if time.Now().Year() < minTrustedYear {
		return false
	}
	if r.AnyPlausible {
		return true
	}
	return fileExists(orDefault(r.SyncMarker, DefaultSyncMarker)) ||
		readTrimmed(orDefault(r.RTCHCToSys, DefaultRTCHCToSys)) == "1"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var _ Clock = Real{}
