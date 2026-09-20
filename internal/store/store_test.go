package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labpower.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labpower.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.SetState(ctx, "k", "v"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open (re-running migrations): %v", err)
	}
	defer s2.Close()

	v, ok, err := s2.GetState(ctx, "k")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !ok || v != "v" {
		t.Fatalf("GetState = (%q, %v), want (\"v\", true) — data lost across reopen", v, ok)
	}
}

func TestStateGetSetUpsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.GetState(ctx, "last_tick"); err != nil || ok {
		t.Fatalf("expected no last_tick set yet, got ok=%v err=%v", ok, err)
	}

	if err := s.SetState(ctx, "last_tick", "100"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s.SetState(ctx, "last_tick", "200"); err != nil {
		t.Fatalf("SetState (update): %v", err)
	}
	v, ok, err := s.GetState(ctx, "last_tick")
	if err != nil || !ok || v != "200" {
		t.Fatalf("GetState = (%q, %v, %v), want (\"200\", true, nil)", v, ok, err)
	}
}

func TestOverrideActiveWithinWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	until := created.Add(2 * time.Hour)
	id, err := s.CreateOverride(ctx, Override{
		Target: "vm-media", Action: "on", Until: &until, CreatedBy: "user:dave", CreatedAt: created,
	})
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero override ID")
	}

	// Before creation: not active.
	if o, err := s.EffectiveOverride(ctx, "vm-media", created.Add(-time.Minute)); err != nil || o != nil {
		t.Fatalf("EffectiveOverride before creation = (%+v, %v), want (nil, nil)", o, err)
	}
	// Within window: active.
	o, err := s.EffectiveOverride(ctx, "vm-media", created.Add(time.Hour))
	if err != nil {
		t.Fatalf("EffectiveOverride: %v", err)
	}
	if o == nil || o.ID != id || o.Action != "on" {
		t.Fatalf("EffectiveOverride = %+v, want the created override", o)
	}
	// After until: no longer active.
	if o, err := s.EffectiveOverride(ctx, "vm-media", until.Add(time.Minute)); err != nil || o != nil {
		t.Fatalf("EffectiveOverride after until = (%+v, %v), want (nil, nil)", o, err)
	}
}

func TestOverrideCancelStopsItImmediately(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	id, err := s.CreateOverride(ctx, Override{
		Target: "vm-media", Action: "on", Until: nil, CreatedBy: "user:dave", CreatedAt: created,
	})
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}

	cancelAt := created.Add(time.Hour)
	if err := s.CancelOverride(ctx, id, cancelAt); err != nil {
		t.Fatalf("CancelOverride: %v", err)
	}

	if o, err := s.EffectiveOverride(ctx, "vm-media", created.Add(30*time.Minute)); err != nil || o == nil {
		t.Fatalf("EffectiveOverride before cancellation = (%+v, %v), want the override still active", o, err)
	}
	if o, err := s.EffectiveOverride(ctx, "vm-media", cancelAt.Add(time.Minute)); err != nil || o != nil {
		t.Fatalf("EffectiveOverride after cancellation = (%+v, %v), want (nil, nil)", o, err)
	}

	if err := s.CancelOverride(ctx, id, cancelAt); err == nil {
		t.Fatal("expected error cancelling an already-cancelled override")
	}
	if err := s.CancelOverride(ctx, 999999, cancelAt); err == nil {
		t.Fatal("expected error cancelling a nonexistent override")
	}
}

func TestEffectiveOverridePicksMostRecent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.CreateOverride(ctx, Override{Target: "vm-media", Action: "on", CreatedBy: "user:dave", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOverride(ctx, Override{Target: "vm-media", Action: "off", CreatedBy: "user:dave", CreatedAt: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	o, err := s.EffectiveOverride(ctx, "vm-media", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("EffectiveOverride: %v", err)
	}
	if o == nil || o.Action != "off" {
		t.Fatalf("EffectiveOverride = %+v, want the more recently created override (action=off)", o)
	}
}

func TestEventsRecordAndList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, kind := range []string{"guest_start", "guest_shutdown", "host_shutdown"} {
		e := Event{
			At: base.Add(time.Duration(i) * time.Minute), Kind: kind, Target: "vm-media",
			Actor: "schedule", DryRun: true,
		}
		if err := s.RecordEvent(ctx, e); err != nil {
			t.Fatalf("RecordEvent(%s): %v", kind, err)
		}
	}

	events, err := s.ListRecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	// Newest first.
	if events[0].Kind != "host_shutdown" || events[2].Kind != "guest_start" {
		t.Fatalf("events not newest-first: %+v", events)
	}
	if !events[0].DryRun {
		t.Error("expected dry_run to round-trip as true")
	}
	if events[0].Target != "vm-media" {
		t.Errorf("Target = %q, want vm-media", events[0].Target)
	}

	limited, err := s.ListRecentEvents(ctx, 1)
	if err != nil {
		t.Fatalf("ListRecentEvents(limit=1): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("got %d events, want 1", len(limited))
	}
}

func TestPruneEventsOlderThan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordEvent(ctx, Event{At: old, Kind: "guest_start", Actor: "schedule"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(ctx, Event{At: recent, Kind: "guest_start", Actor: "schedule"}); err != nil {
		t.Fatal(err)
	}

	if err := s.PruneEventsOlderThan(ctx, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("PruneEventsOlderThan: %v", err)
	}

	events, err := s.ListRecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(events) != 1 || !events[0].At.Equal(recent) {
		t.Fatalf("events after prune = %+v, want only the recent one", events)
	}
}
