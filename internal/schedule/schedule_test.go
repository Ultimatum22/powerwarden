package schedule

import (
	"testing"
	"time"
)

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

func mustWindow(t *testing.T, days, on, off string) Window {
	t.Helper()
	w, err := NewWindow(days, on, off)
	if err != nil {
		t.Fatalf("NewWindow(%q,%q,%q): %v", days, on, off, err)
	}
	return w
}

// findDSTDay scans month for the calendar day whose UTC offset differs
// between just-after-midnight and just-before-next-midnight, i.e. the day
// the clock changes. forward reports whether it's a spring-forward (gains
// offset) or fall-back (loses offset) transition.
func findDSTDay(t *testing.T, loc *time.Location, year int, month time.Month) (day time.Time, forward bool) {
	t.Helper()
	for d := 1; d <= 31; d++ {
		date := time.Date(year, month, d, 0, 0, 0, 0, loc)
		if date.Month() != month {
			break
		}
		next := date.AddDate(0, 0, 1)
		_, offBefore := date.Add(time.Hour).Zone()
		_, offAfter := next.Add(-time.Hour).Zone()
		if offBefore != offAfter {
			return date, offAfter > offBefore
		}
	}
	t.Fatalf("no DST transition found in %s %d", month, year)
	return time.Time{}, false
}

func TestWindowCrossingMidnight(t *testing.T) {
	loc := time.UTC
	// A Monday in a fixed, DST-free zone: 2026-01-05 is a Monday.
	w := mustWindow(t, "mon", "17:00", "00:30")
	sched := Schedule{w}

	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 1, 5, 16, 59, 0, 0, loc), false},
		{time.Date(2026, 1, 5, 17, 0, 0, 0, loc), true},
		{time.Date(2026, 1, 5, 23, 59, 0, 0, loc), true},
		{time.Date(2026, 1, 6, 0, 0, 0, 0, loc), true},
		{time.Date(2026, 1, 6, 0, 29, 0, 0, loc), true},
		{time.Date(2026, 1, 6, 0, 30, 0, 0, loc), false},
		{time.Date(2026, 1, 6, 0, 31, 0, 0, loc), false},
	}
	for _, c := range cases {
		if got := sched.IsOn(c.at, loc); got != c.want {
			t.Errorf("IsOn(%v) = %v, want %v", c.at, got, c.want)
		}
	}
}

func TestDayOfWeekBoundaries(t *testing.T) {
	loc := time.UTC
	w := mustWindow(t, "mon-fri", "07:00", "19:00")
	sched := Schedule{w}

	// 2026-01-05 Mon, 06 Tue, ... 09 Fri, 10 Sat, 11 Sun, 12 Mon.
	if sched.IsOn(time.Date(2026, 1, 10, 8, 0, 0, 0, loc), loc) {
		t.Error("expected Saturday 08:00 to be off (mon-fri window)")
	}
	if !sched.IsOn(time.Date(2026, 1, 9, 18, 59, 0, 0, loc), loc) {
		t.Error("expected Friday 18:59 to be on")
	}

	from := time.Date(2026, 1, 9, 6, 0, 0, 0, loc) // Friday 06:00
	to := time.Date(2026, 1, 12, 8, 0, 0, 0, loc)  // Monday 08:00
	got := sched.Crossings(from, to, loc)
	want := []Boundary{
		{At: time.Date(2026, 1, 9, 7, 0, 0, 0, loc), On: true},
		{At: time.Date(2026, 1, 9, 19, 0, 0, 0, loc), On: false},
		{At: time.Date(2026, 1, 12, 7, 0, 0, 0, loc), On: true},
	}
	assertBoundaries(t, got, want)
}

func TestOverlappingWindows(t *testing.T) {
	loc := time.UTC
	a := mustWindow(t, "mon", "07:00", "12:00")
	b := mustWindow(t, "mon", "10:00", "14:00")
	sched := Schedule{a, b}

	if !sched.IsOn(time.Date(2026, 1, 5, 12, 30, 0, 0, loc), loc) {
		t.Error("expected 12:30 to be on, still covered by the second window")
	}

	from := time.Date(2026, 1, 5, 0, 0, 0, 0, loc)
	to := time.Date(2026, 1, 5, 23, 59, 0, 0, loc)
	got := sched.Crossings(from, to, loc)
	want := []Boundary{
		{At: time.Date(2026, 1, 5, 7, 0, 0, 0, loc), On: true},
		{At: time.Date(2026, 1, 5, 14, 0, 0, 0, loc), On: false},
	}
	assertBoundaries(t, got, want)
}

func TestEmptySchedule(t *testing.T) {
	var sched Schedule
	loc := time.UTC
	if sched.IsOn(time.Now(), loc) {
		t.Error("expected empty schedule to always be off")
	}
	got := sched.Crossings(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), time.Date(2026, 12, 31, 0, 0, 0, 0, loc), loc)
	if len(got) != 0 {
		t.Errorf("expected no crossings for an empty schedule, got %v", got)
	}
}

func TestCrossingsRequiresForwardRange(t *testing.T) {
	loc := time.UTC
	sched := Schedule{mustWindow(t, "mon-sun", "07:00", "19:00")}
	t1 := time.Date(2026, 1, 5, 10, 0, 0, 0, loc)
	if got := sched.Crossings(t1, t1, loc); got != nil {
		t.Errorf("expected nil for a zero-width range, got %v", got)
	}
	if got := sched.Crossings(t1, t1.Add(-time.Hour), loc); got != nil {
		t.Errorf("expected nil for a backwards range, got %v", got)
	}
}

func TestDSTSpringForward(t *testing.T) {
	loc := amsterdam(t)
	day, forward := findDSTDay(t, loc, 2026, time.March)
	if !forward {
		t.Fatalf("expected a spring-forward transition in March, direction reported as fall-back")
	}
	w := mustWindow(t, "mon-sun", "01:00", "04:00")
	sched := Schedule{w}

	from := day.AddDate(0, 0, -1)
	to := day.AddDate(0, 0, 2)
	boundaries := sched.Crossings(from, to, loc)
	if len(boundaries) != 6 {
		t.Fatalf("got %d boundaries, want 6 (on/off for 3 days): %+v", len(boundaries), boundaries)
	}

	// Every "on" boundary must land at 01:00 local, with no cumulative
	// drift from the transition (a fixed-24h day-stepping bug would shift
	// every subsequent day by an hour).
	for i := 0; i < 6; i += 2 {
		b := boundaries[i]
		if !b.On {
			t.Fatalf("boundary %d = %+v, want an On boundary", i, b)
		}
		hh, mm, _ := b.At.In(loc).Clock()
		if hh != 1 || mm != 0 {
			t.Errorf("on boundary %d = %v, want 01:00 local", i, b.At.In(loc))
		}
	}

	// The off boundary on the transition day itself lands an hour later
	// in wall-clock terms (05:00, not 04:00), since one real hour was
	// skipped between 01:00 and 04:00 that day. The days before/after are
	// unaffected.
	wantOffHour := []int{4, 5, 4}
	for i, wantHour := range wantOffHour {
		b := boundaries[i*2+1]
		if b.On {
			t.Fatalf("boundary %d = %+v, want an Off boundary", i*2+1, b)
		}
		hh, _, _ := b.At.In(loc).Clock()
		if hh != wantHour {
			t.Errorf("off boundary for day %d = %v, want hour %d", i, b.At.In(loc), wantHour)
		}
	}
}

func TestDSTFallBack(t *testing.T) {
	loc := amsterdam(t)
	day, forward := findDSTDay(t, loc, 2026, time.October)
	if forward {
		t.Fatalf("expected a fall-back transition in October, direction reported as spring-forward")
	}
	w := mustWindow(t, "mon-sun", "01:00", "04:00")
	sched := Schedule{w}

	from := day.AddDate(0, 0, -1)
	to := day.AddDate(0, 0, 2)
	boundaries := sched.Crossings(from, to, loc)
	if len(boundaries) != 6 {
		t.Fatalf("got %d boundaries, want 6 (on/off for 3 days): %+v", len(boundaries), boundaries)
	}

	for i := 0; i < 6; i += 2 {
		b := boundaries[i]
		hh, mm, _ := b.At.In(loc).Clock()
		if hh != 1 || mm != 0 {
			t.Errorf("on boundary %d = %v, want 01:00 local", i, b.At.In(loc))
		}
	}

	// The off boundary on the transition day lands an hour earlier in
	// wall-clock terms (03:00, not 04:00), since an extra real hour was
	// inserted between 01:00 and 04:00 that day.
	wantOffHour := []int{4, 3, 4}
	for i, wantHour := range wantOffHour {
		b := boundaries[i*2+1]
		hh, _, _ := b.At.In(loc).Clock()
		if hh != wantHour {
			t.Errorf("off boundary for day %d = %v, want hour %d", i, b.At.In(loc), wantHour)
		}
	}
}

func TestParseDaysRejectsUnknown(t *testing.T) {
	if _, err := ParseDays("someday"); err == nil {
		t.Fatal("expected error for unrecognized day spec")
	}
}

func TestParseTimeOfDayRejectsGarbage(t *testing.T) {
	if _, err := ParseTimeOfDay("not-a-time"); err == nil {
		t.Fatal("expected error for invalid time")
	}
	if _, err := ParseTimeOfDay("25:00"); err == nil {
		t.Fatal("expected error for out-of-range hour")
	}
}

func assertBoundaries(t *testing.T, got, want []Boundary) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d boundaries, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || got[i].On != want[i].On {
			t.Errorf("boundary %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// FuzzNewWindow ensures window construction never panics on arbitrary
// input, and that whatever it does accept behaves sanely when evaluated.
func FuzzNewWindow(f *testing.F) {
	f.Add("mon-fri", "07:00", "01:00")
	f.Add("mon-sun", "17:00", "00:30")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, days, on, off string) {
		w, err := NewWindow(days, on, off)
		if err != nil {
			return
		}
		sched := Schedule{w}
		loc := time.UTC
		now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
		_ = sched.IsOn(now, loc)
		_ = sched.Crossings(now, now.AddDate(0, 0, 14), loc)
	})
}
