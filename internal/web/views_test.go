package web

import (
	"testing"
	"time"
)

func TestThunderWindow(t *testing.T) {
	h := newTestServer(t) // Monday 2026-01-05 12:00 UTC, Loc UTC
	at := func(day, hour int) time.Time { return time.Date(2026, 1, day, hour, 0, 0, 0, time.UTC) }
	now := h.Clock.Now()
	tests := []struct {
		name  string
		hours []time.Time
		want  string
	}{
		{"none", nil, ""},
		{"consecutive hours merge", []time.Time{at(5, 18), at(5, 19), at(5, 20), at(5, 21)}, "18:00–22:00"},
		{"first window only", []time.Time{at(5, 14), at(5, 15), at(5, 20)}, "14:00–16:00"},
		{"past hours skipped", []time.Time{at(5, 9), at(5, 10), at(5, 16)}, "16:00–17:00"},
		{"hour in progress counts", []time.Time{at(5, 12)}, "12:00–13:00"},
		{"another day is labelled", []time.Time{at(6, 18)}, "Tue 18:00–19:00"},
	}
	for _, tt := range tests {
		if got := h.thunderWindow(tt.hours, now); got != tt.want {
			t.Errorf("%s: thunderWindow = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStormTrackClipsToRange(t *testing.T) {
	h := newTestServer(t)
	from := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	row := h.stormTrack([]time.Time{from.Add(-time.Hour), from.Add(12 * time.Hour), to.Add(-30 * time.Minute)}, from, to)
	if len(row.Segments) != 2 {
		t.Fatalf("segments = %+v, want the midday hour and the clipped last half hour", row.Segments)
	}
	if s := row.Segments[0]; s.X != 500 || s.W < 41.6 || s.W > 41.7 {
		t.Fatalf("midday segment = %+v, want x=500 w≈41.67", s)
	}
	if s := row.Segments[1]; s.X+s.W > 1000.01 {
		t.Fatalf("last segment %+v runs past the track", s)
	}
}
