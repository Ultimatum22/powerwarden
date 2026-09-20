package engine

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// hostTestSetup gives host-focused tests full control over the host's own
// schedule, retries and wake timeout, unlike newTestEngine (which pins the
// host to an always-on schedule for guest-focused tests).
func hostTestSetup(t *testing.T, hostSchedule schedule.Schedule, guests []GuestConfig, guestSchedules map[string]schedule.Schedule, fc clock.Clock, fp *proxmox.Fake, dryRun bool) *testEngine {
	t.Helper()
	st := openTestStore(t)
	if guestSchedules == nil {
		guestSchedules = map[string]schedule.Schedule{}
	}
	guestSchedules["hostsched"] = hostSchedule

	mac, err := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	wolSender := &fakeWoLSender{}
	notifier := notify.NewFake()

	e, err := New(Config{
		Clock: fc, Proxmox: fp, Store: st, Logger: discardLogger(), DryRun: dryRun, Loc: time.UTC,
		Guests: guests, Schedules: guestSchedules,
		Host:           HostConfig{Schedule: "hostsched"},
		WoLSender:      wolSender,
		WoLMAC:         mac,
		WoLRetries:     3,
		WoLWakeTimeout: time.Minute,
		Notifier:       notifier,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEngine{Engine: e, fp: fp, wolSender: wolSender, notifier: notifier}
}

func offSchedule(t *testing.T) schedule.Schedule {
	t.Helper()
	return mustSchedule(t) // no windows: always off
}

func TestHostWakesWhenUnreachableAndDesired(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	te := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fc, fp, false)

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := te.wolSender.count(); got != 1 {
		t.Fatalf("WoL sends = %d, want 1", got)
	}
	assertStringSlices(t, eventKinds(t, te.Store), []string{"host_wake:host"})
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("notifications = %d, want 1", len(te.notifier.Sent()))
	}
}

func TestHostDoesNotResendBeforeWakeTimeout(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	te := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fc, fp, false)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	fc.Advance(10 * time.Second) // well under the 1-minute wake_timeout
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := te.wolSender.count(); got != 1 {
		t.Fatalf("WoL sends = %d, want 1 (must not resend before wake_timeout elapses)", got)
	}
}

func TestHostResendsAfterWakeTimeoutThenSucceeds(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	te := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fc, fp, false)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil { // attempt 1
		t.Fatalf("tick 1: %v", err)
	}
	fc.Advance(90 * time.Second) // past the 1-minute wake_timeout
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got := te.wolSender.count(); got != 2 {
		t.Fatalf("WoL sends = %d, want 2 (should resend once the first attempt times out)", got)
	}

	// The host comes up.
	fp.NodeReachable = true
	fc.Advance(5 * time.Second)
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}

	// Further ticks must not send any more WoL packets, even well past
	// another wake_timeout.
	fc.Advance(2 * time.Minute)
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick 4: %v", err)
	}
	if got := te.wolSender.count(); got != 2 {
		t.Fatalf("WoL sends after reaching On = %d, want still 2", got)
	}
}

func TestHostWakeGivesUpAfterRetriesExhausted(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	te := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fc, fp, false) // WoLRetries: 3
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil { // attempt 1
		t.Fatalf("tick: %v", err)
	}
	for i := 0; i < 3; i++ { // 3 more timeouts: attempts 2, 3, then give up
		fc.Advance(90 * time.Second)
		if err := te.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if got := te.wolSender.count(); got != 3 {
		t.Fatalf("WoL sends = %d, want 3 (1 initial + 2 retries, then give up)", got)
	}

	// Further ticks must not send more, or re-notify.
	fc.Advance(2 * time.Minute)
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after giving up: %v", err)
	}
	if got := te.wolSender.count(); got != 3 {
		t.Fatalf("WoL sends after giving up = %d, want still 3", got)
	}

	kinds := eventKinds(t, te.Store)
	if len(kinds) == 0 || kinds[len(kinds)-1] != "host_wake_failed:host" {
		t.Fatalf("events = %v, want the last one to be host_wake_failed:host", kinds)
	}
	notes := te.notifier.Sent()
	if len(notes) == 0 || notes[len(notes)-1].Priority != notify.PriorityUrgent {
		t.Fatalf("expected the final notification to be urgent, got %+v", notes)
	}
}

func TestHostShutsDownWhenNoLongerDesired(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = true
	te := hostTestSetup(t, offSchedule(t), nil, nil, fc, fp, false)

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if fp.NodeReachable {
		t.Fatal("expected the host to have been shut down")
	}
	assertStringSlices(t, eventKinds(t, te.Store), []string{"host_shutdown:host"})
}

func TestHostShutdownForceStopsGuestsRegardlessOfTheirOwnSchedule(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = true
	// vm-media's own schedule says off (so it doesn't factor into
	// anyGuestWantsOn, and the host schedule's "off" is what triggers
	// shutdown), but it's actually running — e.g. started manually via the
	// Proxmox UI. Ordinary per-guest reconciliation would leave a
	// manually-started guest alone until its own next boundary, but the
	// host going down must take it with it regardless.
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-media", Kind: proxmox.KindQEMU, Status: proxmox.StatusRunning})

	te := hostTestSetup(t, offSchedule(t),
		[]GuestConfig{{Name: "vm-media", Schedule: "guestsched"}},
		map[string]schedule.Schedule{"guestsched": offSchedule(t)},
		fc, fp, false)

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatal("expected vm-media to be force-stopped as part of the host shutdown")
	}
}

func TestHostShutdownPostponedByActiveTasksAndNotifiedOnce(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = true
	fp.SetActiveTasks([]proxmox.Task{{UPID: "UPID:pve01:backup", Type: "vzdump"}})
	te := hostTestSetup(t, offSchedule(t), nil, nil, fc, fp, false)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := te.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		fc.Advance(30 * time.Second)
	}
	if !fp.NodeReachable {
		t.Fatal("expected the host to still be up while a task is active")
	}
	if len(te.notifier.Sent()) != 1 {
		t.Fatalf("notifications = %d, want exactly 1 (not renotified every tick)", len(te.notifier.Sent()))
	}

	fp.SetActiveTasks(nil)
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("final tick: %v", err)
	}
	if fp.NodeReachable {
		t.Fatal("expected the host to shut down once the task finished")
	}
}

func TestHostOverridePauseLeavesItAlone(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = true
	te := hostTestSetup(t, offSchedule(t), nil, nil, fc, fp, false) // schedule says off

	if _, err := te.Store.CreateOverride(context.Background(), store.Override{
		Target: hostTarget, Action: "pause", CreatedBy: "user:dave", CreatedAt: fc.Now(),
	}); err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !fp.NodeReachable {
		t.Fatal("expected a paused host override to prevent shutdown")
	}
	if got := eventKinds(t, te.Store); len(got) != 0 {
		t.Fatalf("expected no host actions while paused, got %v", got)
	}
}

func TestHostOverrideOnWakesItEvenIfScheduleSaysOff(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	te := hostTestSetup(t, offSchedule(t), nil, nil, fc, fp, false)

	if _, err := te.Store.CreateOverride(context.Background(), store.Override{
		Target: hostTarget, Action: "on", CreatedBy: "user:dave", CreatedAt: fc.Now(),
	}); err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := te.wolSender.count(); got != 1 {
		t.Fatalf("WoL sends = %d, want 1 (an \"on\" override must wake the host despite the schedule)", got)
	}
}

func TestAnyGuestWantingOnWakesTheHost(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.NodeReachable = false
	guestSched := alwaysOnSchedule(t) // the guest wants on even though the host schedule says off
	te := hostTestSetup(t, offSchedule(t),
		[]GuestConfig{{Name: "vm-media", Schedule: "guestsched"}},
		map[string]schedule.Schedule{"guestsched": guestSched},
		fc, fp, false)

	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := te.wolSender.count(); got != 1 {
		t.Fatalf("WoL sends = %d, want 1 (a guest wanting on must wake the host)", got)
	}
}

func TestHostDryRunNeverSendsRealWoLOrShutdown(t *testing.T) {
	fcWake := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fpWake := proxmox.NewFake()
	fpWake.NodeReachable = false
	teWake := hostTestSetup(t, alwaysOnSchedule(t), nil, nil, fcWake, fpWake, true /* dry-run */)
	if err := teWake.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (wake): %v", err)
	}
	if got := teWake.wolSender.count(); got != 0 {
		t.Fatalf("dry-run WoL sends = %d, want 0", got)
	}
	assertStringSlices(t, eventKinds(t, teWake.Store), []string{"host_wake:host"})

	fcShutdown := clock.NewFake(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	fpShutdown := proxmox.NewFake()
	fpShutdown.NodeReachable = true
	teShutdown := hostTestSetup(t, offSchedule(t), nil, nil, fcShutdown, fpShutdown, true)
	if err := teShutdown.Tick(context.Background()); err != nil {
		t.Fatalf("Tick (shutdown): %v", err)
	}
	if !fpShutdown.NodeReachable {
		t.Fatal("dry-run must not actually shut down the host")
	}
	assertStringSlices(t, eventKinds(t, teShutdown.Store), []string{"host_shutdown:host"})
}
