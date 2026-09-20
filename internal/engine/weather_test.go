package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

func newWeatherTestEngine(t *testing.T, wcfg weather.Config) *testEngine {
	t.Helper()
	loc := time.UTC
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, loc))
	fp := proxmox.NewFake()
	te := newTestEngine(t, nil, nil, fc, fp, openTestStore(t), false, loc)
	te.Weather = weather.NewMonitor(wcfg, fc.Now())
	return te
}

func TestWeatherLevelChangeRecordsEventAndNotifiesAboveWarning(t *testing.T) {
	forecast := &weather.FakeForecast{}
	wcfg := weather.Config{Forecast: forecast}
	te := newWeatherTestEngine(t, wcfg)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 1 (normal): %v", err)
	}
	if got := eventKinds(t, te.Store); len(got) != 0 {
		t.Fatalf("expected no weather event while staying Normal, got %v", got)
	}
	if len(te.notifier.Sent()) != 0 {
		t.Fatalf("expected no notification for Normal, got %v", te.notifier.Sent())
	}

	// Escalate to Warning via an official orange thunderstorm warning.
	now := te.Clock.Now()
	warnings := &weather.FakeWarnings{Warnings: []weather.AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(time.Hour)},
	}}
	te.Config.Weather = weather.NewMonitor(weather.Config{Forecast: forecast, Warnings: warnings, WarningRadiusKM: 30, DangerRadiusKM: 12}, now)

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 2 (warning): %v", err)
	}
	events := eventKinds(t, te.Store)
	if len(events) != 1 || events[0] != "weather_level:" {
		t.Fatalf("events = %v, want one weather_level event", events)
	}
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("expected 1 notification entering Warning, got %d: %+v", len(te.notifier.Sent()), te.notifier.Sent())
	}
}

func TestWeatherLevelUnchangedDoesNotReNotify(t *testing.T) {
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	warnings := &weather.FakeWarnings{Warnings: []weather.AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(4 * time.Hour)},
	}}
	te := newWeatherTestEngine(t, weather.Config{Warnings: warnings, WarningRadiusKM: 30, DangerRadiusKM: 12})
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got := eventKinds(t, te.Store); len(got) != 1 {
		t.Fatalf("expected exactly 1 weather event across two ticks at the same level, got %v", got)
	}
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", len(te.notifier.Sent()))
	}
}

func TestWeatherReturningBelowWarningAlsoNotifies(t *testing.T) {
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	warnings := &weather.FakeWarnings{Warnings: []weather.AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(time.Hour)},
	}}
	te := newWeatherTestEngine(t, weather.Config{Warnings: warnings, WarningRadiusKM: 30, DangerRadiusKM: 12})
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 1 (warning): %v", err)
	}

	// The warning clears.
	warnings.Warnings = nil
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 2 (all clear): %v", err)
	}

	if got := eventKinds(t, te.Store); len(got) != 2 {
		t.Fatalf("expected 2 weather events (into and out of Warning), got %v", got)
	}
	if len(te.notifier.Sent()) != 2 {
		t.Fatalf("expected a notification for both the escalation and the all-clear, got %d", len(te.notifier.Sent()))
	}
}

func TestWeatherStaleDuringNormalNotifiesOncePerEpisode(t *testing.T) {
	forecast := &weather.FakeForecast{Err: context.DeadlineExceeded}
	wcfg := weather.Config{Forecast: forecast, StaleAfter: time.Minute}
	loc := time.UTC
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, loc))
	fp := proxmox.NewFake()
	te := newTestEngine(t, nil, nil, fc, fp, openTestStore(t), false, loc)
	te.Weather = weather.NewMonitor(wcfg, fc.Now())
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(te.notifier.Sent()) != 0 {
		t.Fatalf("expected no stale notification within the grace period, got %d", len(te.notifier.Sent()))
	}

	fc.Advance(2 * time.Minute) // past stale_after, forecast never succeeds
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("expected exactly 1 stale notification, got %d", len(te.notifier.Sent()))
	}

	fc.Advance(30 * time.Second)
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("expected still exactly 1 stale notification (not renotified every tick), got %d", len(te.notifier.Sent()))
	}
}

func TestNilWeatherMonitorIsANoOp(t *testing.T) {
	loc := time.UTC
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, loc))
	fp := proxmox.NewFake()
	te := newTestEngine(t, nil, nil, fc, fp, openTestStore(t), false, loc)
	te.Weather = nil // explicit: weather disabled entirely

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick with no weather monitor configured: %v", err)
	}
	if got := eventKinds(t, te.Store); len(got) != 0 {
		t.Fatalf("expected no weather events when disabled, got %v", got)
	}
}

func TestWeatherNeverBlocksGuestReconciliation(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})
	fc := clock.NewFake(time.Date(2026, 1, 5, 7, 0, 0, 0, loc)) // on-boundary
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime}, fc, fp, openTestStore(t), false, loc)

	// Danger-level weather (a strike right on top of the house), but
	// milestone 5 ships notify-only: it must not stop the guest from
	// starting on schedule.
	te.Weather = weather.NewMonitor(weather.Config{
		Lightning:       &weather.FakeLightning{StrikesData: []weather.Strike{{At: fc.Now(), Point: weather.Point{}}}},
		WarningRadiusKM: 30, DangerRadiusKM: 12,
	}, fc.Now())

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusRunning {
		t.Fatal("expected the guest to start on schedule despite Danger-level weather (notify-only milestone)")
	}
}
