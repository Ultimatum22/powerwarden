package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/store"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

// newEnforceTestEngine builds an engine with weather.mode=enforce, an
// always-on host/guest schedule (so absent weather, the host would stay
// up), and a controllable weather.Monitor.
func newEnforceTestEngine(t *testing.T, warningCountdown, shutdownGrace time.Duration) (*testEngine, *weather.FakeLightning, *weather.FakeWarnings) {
	t.Helper()
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = true

	lightning := &weather.FakeLightning{}
	warnings := &weather.FakeWarnings{}
	monitor := weather.NewMonitor(weather.Config{
		Location: weather.Point{}, Lightning: lightning, Warnings: warnings,
		WarningRadiusKM: 30, DangerRadiusKM: 12,
	}, fc.Now())

	te := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fc, fp, false)
	te.Weather = monitor
	te.WeatherMode = "enforce"
	te.WeatherWarningCountdown = warningCountdown
	te.Host.ShutdownGrace = shutdownGrace
	return te, lightning, warnings
}

func warningAt(now time.Time) []weather.AlertWarning {
	return []weather.AlertWarning{{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(4 * time.Hour)}}
}

func TestWeatherEnforceWarningShutsDownAfterCountdown(t *testing.T) {
	te, _, warnings := newEnforceTestEngine(t, 10*time.Minute, time.Minute)
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil { // bootstrap: host up, no weather yet
		t.Fatalf("bootstrap tick: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected host to be up before any warning")
	}

	warnings.Warnings = warningAt(fc.Now())
	if err := te.Tick(ctx); err != nil { // Warning starts
		t.Fatalf("tick at warning onset: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected the host to stay up before the countdown elapses")
	}

	fc.Advance(5 * time.Minute) // still under the 10-minute countdown
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick mid-countdown: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected the host to still be up mid-countdown")
	}

	fc.Advance(6 * time.Minute) // now past the 10-minute countdown
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after countdown: %v", err)
	}
	if te.fp.NodeReachable {
		t.Fatal("expected the host to shut down once the warning countdown elapsed")
	}
}

func TestWeatherEnforceDangerShutsDownImmediately(t *testing.T) {
	te, lightning, _ := newEnforceTestEngine(t, 10*time.Minute, time.Minute)
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}

	// A strike right on top of the house: Danger.
	lightning.StrikesData = []weather.Strike{{At: fc.Now(), Point: weather.Point{}}}
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at danger onset: %v", err)
	}
	if te.fp.NodeReachable {
		t.Fatal("expected Danger to shut the host down immediately, no countdown")
	}
}

func TestWeatherEnforceIgnoreOverrideBypassesShutdown(t *testing.T) {
	te, lightning, _ := newEnforceTestEngine(t, 10*time.Minute, time.Minute)
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}

	until := fc.Now().Add(time.Hour)
	if _, err := te.Store.CreateOverride(ctx, store.Override{
		Target: hostTarget, Action: "ignore_weather", Until: &until, CreatedBy: "user:dave", CreatedAt: fc.Now(),
	}); err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}

	lightning.StrikesData = []weather.Strike{{At: fc.Now(), Point: weather.Point{}}} // Danger
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at danger with ignore_weather active: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected an active ignore_weather override to bypass the Danger shutdown")
	}
}

func TestWeatherEnforceDangerBypassesActiveTasksAfterGrace(t *testing.T) {
	te, lightning, _ := newEnforceTestEngine(t, 10*time.Minute, 2*time.Minute)
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}
	te.fp.SetActiveTasks([]proxmox.Task{{UPID: "UPID:pve01:backup", Type: "vzdump"}})

	lightning.StrikesData = []weather.Strike{{At: fc.Now(), Point: weather.Point{}}} // Danger
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at danger onset with active task: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected the host to still be up within the shutdown_grace period despite Danger")
	}

	fc.Advance(3 * time.Minute) // past the 2-minute grace period
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after grace period: %v", err)
	}
	if te.fp.NodeReachable {
		t.Fatal("expected the host to shut down despite the active task once the grace period elapsed")
	}
}

func TestWeatherEnforceAllClearReturnsToSchedule(t *testing.T) {
	te, lightning, _ := newEnforceTestEngine(t, 10*time.Minute, time.Minute)
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}
	lightning.StrikesData = []weather.Strike{{At: fc.Now(), Point: weather.Point{}}} // Danger
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at danger: %v", err)
	}
	if te.fp.NodeReachable {
		t.Fatal("sanity check failed: expected the host to be down after Danger")
	}

	lightning.StrikesData = nil // all clear
	sentBefore := te.wolSender.count()
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after all-clear: %v", err)
	}
	if te.wolSender.count() <= sentBefore {
		t.Fatal("expected the engine to attempt waking the host once weather cleared (schedule wants it on)")
	}

	// Simulate the host actually powering back on in response, then
	// confirm weather no longer forces it back off.
	te.fp.NodeReachable = true
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick once host is back up: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected the host to stay up once weather cleared and it's reachable again")
	}
}

func TestWeatherNotifyModeNeverEnforces(t *testing.T) {
	te, lightning, _ := newEnforceTestEngine(t, 10*time.Minute, time.Minute)
	te.WeatherMode = "notify" // explicitly not enforce
	fc := te.Clock.(*clock.Fake)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}
	lightning.StrikesData = []weather.Strike{{At: fc.Now(), Point: weather.Point{}}} // Danger
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at danger: %v", err)
	}
	if !te.fp.NodeReachable {
		t.Fatal("expected notify-only mode to never shut the host down, even at Danger")
	}
}
