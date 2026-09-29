package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/schedule"
)

// Schedule builds the named schedule. Callers must only use it after
// Validate has accepted the config.
func (c *Config) Schedule(name string) (schedule.Schedule, error) {
	windows, ok := c.Schedules[name]
	if !ok {
		return nil, fmt.Errorf("schedule %q is not defined", name)
	}
	s := make(schedule.Schedule, 0, len(windows))
	for _, w := range windows {
		win, err := schedule.NewWindow(w.Days, w.On, w.Off)
		if err != nil {
			return nil, err
		}
		s = append(s, win)
	}
	return s, nil
}

// referenceWeek is a Monday-to-Monday week with no DST transition in
// either hemisphere's usual rules, used to compare schedules minute by
// minute. Coverage questions ("is the host on whenever the guest is?")
// don't depend on which week is checked; DST edge cases are the
// schedule package's own concern and are tested there.
var referenceWeek = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// firstGap returns the first minute of the reference week where inner is
// on but outer is off.
func firstGap(inner, outer schedule.Schedule, loc *time.Location) (time.Time, bool) {
	start := time.Date(referenceWeek.Year(), referenceWeek.Month(), referenceWeek.Day(), 0, 0, 0, 0, loc)
	for t := start; t.Before(start.AddDate(0, 0, 7)); t = t.Add(time.Minute) {
		if inner.IsOn(t, loc) && !outer.IsOn(t, loc) {
			return t, true
		}
	}
	return time.Time{}, false
}

func formatWeekMinute(t time.Time) string {
	return strings.ToLower(t.Format("Mon 15:04"))
}

// validateCoverage rejects guest windows that fall outside the host's
// schedule (CLAUDE.md: validation must reject "guest windows outside the
// host window"), since the guest can't run while its host is off.
func (c *Config) validateCoverage(loc *time.Location) []error {
	host, err := c.Schedule(c.Host.Schedule)
	if err != nil {
		return nil // reported by validateHost
	}
	names := make([]string, 0, len(c.Guests))
	for name := range c.Guests {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		g := c.Guests[name]
		if g.AlwaysOn || g.Schedule == "" {
			continue
		}
		gs, err := c.Schedule(g.Schedule)
		if err != nil {
			continue // reported by validateGuests
		}
		if at, ok := firstGap(gs, host, loc); ok {
			errs = append(errs, fmt.Errorf("guests.%s: schedule %q is on at %s but host schedule %q is off then; guest windows must fall inside the host window",
				name, g.Schedule, formatWeekMinute(at), c.Host.Schedule))
		}
	}
	return errs
}

// Warnings reports problems that don't stop labpower from running but
// that the owner should see: currently, required_windows (e.g. the weekly
// Trivy scan) during which the host schedule has the host off. Callers
// must only use it after Validate has accepted the config.
func (c *Config) Warnings() []string {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil
	}
	host, err := c.Schedule(c.Host.Schedule)
	if err != nil {
		return nil
	}
	var out []string
	if c.TrustAnyPlausibleClock() {
		out = append(out, `clock.trust is "system": the engine acts on any plausible system time, even an unsynchronised one (development only)`)
	}
	for _, rw := range c.RequiredWindows {
		// A required window is "host must be on from From to To on Days",
		// which is exactly a schedule window.
		req, err := schedule.NewWindow(rw.Days, rw.From, rw.To)
		if err != nil {
			continue
		}
		if at, ok := firstGap(schedule.Schedule{req}, host, loc); ok {
			out = append(out, fmt.Sprintf("required window %q (%s %s–%s): host schedule %q has the host off at %s",
				rw.Name, rw.Days, rw.From, rw.To, c.Host.Schedule, formatWeekMinute(at)))
		}
	}
	return out
}
