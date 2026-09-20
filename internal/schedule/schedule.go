// Package schedule computes on/off windows and the boundaries between them.
// Every function here is pure (no I/O, no wall-clock reads) so the engine
// can replay a simulated week in milliseconds and so DST transitions can be
// tested deterministically.
package schedule

import (
	"fmt"
	"sort"
	"time"
)

// Days is a bitmask of weekdays a window applies to.
type Days uint8

const (
	Monday Days = 1 << iota
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
	Sunday
)

// Weekdays and Weekend are the two named ranges the config format accepts
// as shorthand, plus the individual days and the full week.
const (
	Weekdays = Monday | Tuesday | Wednesday | Thursday | Friday
	Weekend  = Saturday | Sunday
	AllDays  = Weekdays | Weekend
)

func dayBit(d time.Weekday) Days {
	switch d {
	case time.Monday:
		return Monday
	case time.Tuesday:
		return Tuesday
	case time.Wednesday:
		return Wednesday
	case time.Thursday:
		return Thursday
	case time.Friday:
		return Friday
	case time.Saturday:
		return Saturday
	default:
		return Sunday
	}
}

// Has reports whether d includes weekday w.
func (d Days) Has(w time.Weekday) bool {
	return d&dayBit(w) != 0
}

// ParseDays parses the config day-spec shorthand: "mon-fri", "sat-sun",
// "mon-sun", or a single day ("mon".."sun").
func ParseDays(spec string) (Days, error) {
	switch spec {
	case "mon-fri":
		return Weekdays, nil
	case "sat-sun":
		return Weekend, nil
	case "mon-sun":
		return AllDays, nil
	case "mon":
		return Monday, nil
	case "tue":
		return Tuesday, nil
	case "wed":
		return Wednesday, nil
	case "thu":
		return Thursday, nil
	case "fri":
		return Friday, nil
	case "sat":
		return Saturday, nil
	case "sun":
		return Sunday, nil
	default:
		return 0, fmt.Errorf("schedule: %q is not a recognized day spec", spec)
	}
}

// TimeOfDay is an offset from local midnight, in [0, 24h).
type TimeOfDay time.Duration

// ParseTimeOfDay parses "HH:MM".
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("schedule: %q must be HH:MM", s)
	}
	return TimeOfDay(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute), nil
}

// Window is one on/off entry: guests/hosts following it are "on" from On to
// Off (wrapping past midnight if Off <= On) on every day in Days.
type Window struct {
	Days Days
	On   TimeOfDay
	Off  TimeOfDay
}

// NewWindow builds a Window from the config's string representation.
func NewWindow(days, on, off string) (Window, error) {
	d, err := ParseDays(days)
	if err != nil {
		return Window{}, err
	}
	onT, err := ParseTimeOfDay(on)
	if err != nil {
		return Window{}, err
	}
	offT, err := ParseTimeOfDay(off)
	if err != nil {
		return Window{}, err
	}
	return Window{Days: d, On: onT, Off: offT}, nil
}

// wraps reports whether the window's off time falls on the calendar day
// after its on time.
func (w Window) wraps() bool {
	return w.Off <= w.On
}

// instance is one concrete occurrence of a window: the on/off instants for
// one calendar day it applies to.
type instance struct {
	on, off time.Time
}

// instanceStarting returns the window's instance starting on the calendar
// day of dayStart (which must be local midnight in loc), or ok=false if the
// window doesn't apply on that day.
func (w Window) instanceStarting(dayStart time.Time, loc *time.Location) (instance, bool) {
	if !w.Days.Has(dayStart.Weekday()) {
		return instance{}, false
	}
	on := dayStart.Add(time.Duration(w.On))
	var off time.Time
	if w.wraps() {
		nextDay := midnight(dayStart.AddDate(0, 0, 1), loc)
		off = nextDay.Add(time.Duration(w.Off))
	} else {
		off = dayStart.Add(time.Duration(w.Off))
	}
	return instance{on: on, off: off}, true
}

// midnight returns local midnight, in loc, of the calendar day t falls on.
func midnight(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Schedule is a named set of windows; a target is "on" whenever any window
// says so (windows may overlap).
type Schedule []Window

// instancesCovering returns every window instance whose on/off pair could
// possibly overlap [from, to], i.e. starting on any day from the day before
// "from" through the day of "to" (a window can start the previous day and
// wrap past midnight into today).
func (s Schedule) instancesCovering(from, to time.Time, loc *time.Location) []instance {
	if len(s) == 0 {
		return nil
	}
	start := midnight(from, loc).AddDate(0, 0, -1)
	end := midnight(to, loc)

	var out []instance
	for day := start; !day.After(end); day = midnight(day.AddDate(0, 0, 1), loc) {
		for _, w := range s {
			if inst, ok := w.instanceStarting(day, loc); ok {
				out = append(out, inst)
			}
		}
	}
	return out
}

// IsOn reports whether the schedule says "on" at instant t, evaluated in loc.
func (s Schedule) IsOn(t time.Time, loc *time.Location) bool {
	for _, inst := range s.instancesCovering(t, t, loc) {
		if !t.Before(inst.on) && t.Before(inst.off) {
			return true
		}
	}
	return false
}

// Boundary is a single point where the schedule's aggregate on/off state
// changes.
type Boundary struct {
	At time.Time
	On bool // true: turning on; false: turning off
}

// Crossings returns every boundary in (from, to], in chronological order.
// Overlapping windows are merged: a boundary is only reported where the
// combined on/off state actually changes, not at every individual window's
// edge.
func (s Schedule) Crossings(from, to time.Time, loc *time.Location) []Boundary {
	if !to.After(from) || len(s) == 0 {
		return nil
	}

	instances := s.instancesCovering(from, to, loc)
	candidateSet := make(map[int64]time.Time)
	for _, inst := range instances {
		for _, t := range []time.Time{inst.on, inst.off} {
			if t.After(from) && !t.After(to) {
				candidateSet[t.UnixNano()] = t
			}
		}
	}
	if len(candidateSet) == 0 {
		return nil
	}
	candidates := make([]time.Time, 0, len(candidateSet))
	for _, t := range candidateSet {
		candidates = append(candidates, t)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })

	prev := s.IsOn(from, loc)
	out := make([]Boundary, 0, len(candidates))
	for _, c := range candidates {
		cur := s.IsOn(c, loc)
		if cur != prev {
			out = append(out, Boundary{At: c, On: cur})
			prev = cur
		}
	}
	return out
}
