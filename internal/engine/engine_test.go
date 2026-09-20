package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labpower.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustSchedule(t *testing.T, windows ...[3]string) schedule.Schedule {
	t.Helper()
	var sched schedule.Schedule
	for _, w := range windows {
		win, err := schedule.NewWindow(w[0], w[1], w[2])
		if err != nil {
			t.Fatalf("NewWindow: %v", err)
		}
		sched = append(sched, win)
	}
	return sched
}

// alwaysOnSchedule is mon-sun 00:00-00:00 (wraps to a full 24h span, see
// schedule.Window), used as the host's schedule in tests that don't care
// about host behavior, so the host state machine stays in its steady
// "reachable and desired" state and never interferes with what the test
// actually wants to exercise.
func alwaysOnSchedule(t *testing.T) schedule.Schedule {
	return mustSchedule(t, [3]string{"mon-sun", "00:00", "00:00"})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// fakeWoLSender records Wake-on-LAN sends for tests instead of touching
// the network.
type fakeWoLSender struct {
	mu   sync.Mutex
	sent int
}

func (f *fakeWoLSender) Send(_ context.Context, _ net.HardwareAddr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent++
	return nil
}

func (f *fakeWoLSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

// testEngine bundles an Engine with the fakes so host-focused tests can
// reach into them (fp, wolSender, notifier) without re-deriving what
// newTestEngine built.
type testEngine struct {
	*Engine
	fp        *proxmox.Fake
	wolSender *fakeWoLSender
	notifier  *notify.Fake
}

// newTestEngine builds an Engine with a host schedule that's always on by
// default, so guest-focused tests don't need to think about host wake/
// shutdown at all. Host-focused tests override cfg.Host / reachability
// themselves via the returned testEngine's fields.
func newTestEngine(t *testing.T, guests []GuestConfig, schedules map[string]schedule.Schedule, fc clock.Clock, fp *proxmox.Fake, st *store.Store, dryRun bool, loc *time.Location) *testEngine {
	t.Helper()
	if schedules == nil {
		schedules = map[string]schedule.Schedule{}
	}
	schedules["alwayson"] = alwaysOnSchedule(t)

	mac, err := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	wolSender := &fakeWoLSender{}
	notifier := notify.NewFake()

	e, err := New(Config{
		Clock: fc, Proxmox: fp, Store: st, Logger: discardLogger(), DryRun: dryRun, Loc: loc,
		Guests: guests, Schedules: schedules,
		Host:           HostConfig{Schedule: "alwayson"},
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

// orderTrackingProxmox wraps a Fake to record the order start/shutdown
// calls arrive in, so dependency-ordering tests can verify it.
type orderTrackingProxmox struct {
	*proxmox.Fake
	order *[]string
}

func (o orderTrackingProxmox) StartGuest(ctx context.Context, kind proxmox.GuestKind, vmid int) (proxmox.UPID, error) {
	*o.order = append(*o.order, fmt.Sprintf("start:%d", vmid))
	return o.Fake.StartGuest(ctx, kind, vmid)
}

func (o orderTrackingProxmox) ShutdownGuest(ctx context.Context, kind proxmox.GuestKind, vmid int) (proxmox.UPID, error) {
	*o.order = append(*o.order, fmt.Sprintf("shutdown:%d", vmid))
	return o.Fake.ShutdownGuest(ctx, kind, vmid)
}

func eventKinds(t *testing.T, st *store.Store) []string {
	t.Helper()
	events, err := st.ListRecentEvents(context.Background(), 1000)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	// oldest first
	out := make([]string, len(events))
	for i, e := range events {
		out[len(events)-1-i] = e.Kind + ":" + e.Target
	}
	return out
}

func TestWeekSimulationMatchesExpectedActions(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})

	fc := clock.NewFake(time.Date(2026, 1, 5, 0, 0, 0, 0, loc)) // Monday
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime},
		fc, fp, st, false /* live, so we can assert real state changes */, loc)

	ctx := context.Background()
	for h := 0; h < 7*24; h++ {
		if err := te.Tick(ctx); err != nil {
			t.Fatalf("Tick at hour %d (%v): %v", h, fc.Now(), err)
		}
		fc.Advance(time.Hour)
	}

	got := eventKinds(t, st)
	want := []string{
		"guest_start:vm-work", "guest_shutdown:vm-work", // Mon
		"guest_start:vm-work", "guest_shutdown:vm-work", // Tue
		"guest_start:vm-work", "guest_shutdown:vm-work", // Wed
		"guest_start:vm-work", "guest_shutdown:vm-work", // Thu
		"guest_start:vm-work", "guest_shutdown:vm-work", // Fri
		// no action Sat/Sun
	}
	assertStringSlices(t, got, want)
}

func TestCatchUpAppliesMostRecentStateOnce(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})

	fc := clock.NewFake(time.Date(2026, 1, 5, 6, 0, 0, 0, loc)) // Monday 06:00, before the window
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime}, fc, fp, st, false, loc)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil {
		t.Fatalf("bootstrap tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatalf("expected guest to remain stopped before the window, got %v", g.Status)
	}

	// Simulate the Pi being off from Monday 06:00 to Wednesday 10:00: the
	// schedule crossed 5 boundaries in between (Mon on/off, Tue on/off,
	// Wed on), but only the current desired state should be applied once.
	fc.Set(time.Date(2026, 1, 7, 10, 0, 0, 0, loc)) // Wednesday 10:00
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("catch-up tick: %v", err)
	}

	if g, _ := fp.Guest(201); g.Status != proxmox.StatusRunning {
		t.Fatalf("expected guest running after catch-up, got %v", g.Status)
	}
	got := eventKinds(t, st)
	want := []string{"guest_start:vm-work"}
	assertStringSlices(t, got, want)
}

func TestManualStartStaysOnUntilNextBoundaryThenScheduleResumes(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})

	fc := clock.NewFake(time.Date(2026, 1, 5, 22, 0, 0, 0, loc)) // Monday 22:00, off-window
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime}, fc, fp, st, false, loc)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil { // bootstrap: schedule off, guest off, no-op
		t.Fatalf("bootstrap tick: %v", err)
	}

	// Someone starts it directly in the Proxmox UI, bypassing labpower.
	if _, err := fp.StartGuest(ctx, proxmox.KindQEMU, 201); err != nil {
		t.Fatalf("simulated manual start: %v", err)
	}

	// An hour later, still no schedule boundary: must stay on.
	fc.Set(time.Date(2026, 1, 5, 23, 0, 0, 0, loc))
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after manual start: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusRunning {
		t.Fatal("expected the manually-started guest to remain running")
	}
	if got := eventKinds(t, st); len(got) != 0 {
		t.Fatalf("expected no engine actions yet, got %v", got)
	}

	// Tuesday 07:00: the next boundary (an "on"). Schedule resumes
	// authority but the guest already matches, so still no action.
	fc.Set(time.Date(2026, 1, 6, 7, 0, 0, 0, loc))
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at next on-boundary: %v", err)
	}
	if got := eventKinds(t, st); len(got) != 0 {
		t.Fatalf("expected no engine actions at the matching on-boundary, got %v", got)
	}

	// Tuesday 19:00: schedule's off-boundary. Now that it's back under
	// schedule authority, it must shut down like any other day.
	fc.Set(time.Date(2026, 1, 6, 19, 0, 0, 0, loc))
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick at off-boundary: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatal("expected the guest to be shut down at the scheduled off-boundary")
	}
	assertStringSlices(t, eventKinds(t, st), []string{"guest_shutdown:vm-work"})
}

func TestOverrideExpiryRevertsToSchedule(t *testing.T) {
	loc := time.UTC
	night := mustSchedule(t, [3]string{"mon-sun", "02:00", "03:00"}) // otherwise off all day

	start := time.Date(2026, 1, 5, 10, 0, 0, 0, loc) // Monday 10:00
	fc := clock.NewFake(start)
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	until := start.Add(2 * time.Hour) // 12:00
	if _, err := st.CreateOverride(context.Background(), store.Override{
		Target: "vm-work", Action: "on", Until: &until, CreatedBy: "user:dave", CreatedAt: start,
	}); err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "night"}},
		map[string]schedule.Schedule{"night": night}, fc, fp, st, false, loc)
	ctx := context.Background()

	if err := te.Tick(ctx); err != nil { // bootstrap: override active, must start
		t.Fatalf("bootstrap tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusRunning {
		t.Fatal("expected the override to start the guest")
	}

	fc.Set(start.Add(time.Hour)) // 11:00, still within the override
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick within override: %v", err)
	}
	if got := eventKinds(t, st); len(got) != 1 {
		t.Fatalf("expected no new actions while the override holds, got %v", got)
	}

	fc.Set(start.Add(150 * time.Minute)) // 12:30, override has expired, no schedule boundary either
	if err := te.Tick(ctx); err != nil {
		t.Fatalf("tick after override expiry: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatal("expected the guest to be shut down once the override expired")
	}
	assertStringSlices(t, eventKinds(t, st), []string{"guest_start:vm-work", "guest_shutdown:vm-work"})
}

func TestDependencyOrdering(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})

	fc := clock.NewFake(time.Date(2026, 1, 5, 6, 0, 0, 0, loc)) // Monday 06:00
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 100, Name: "lxc-base", Kind: proxmox.KindLXC, Status: proxmox.StatusStopped})
	fp.AddGuest(proxmox.Guest{VMID: 101, Name: "lxc-app", Kind: proxmox.KindLXC, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	var order []string
	tracked := orderTrackingProxmox{Fake: fp, order: &order}

	schedules := map[string]schedule.Schedule{"daytime": daytime, "alwayson": alwaysOnSchedule(t)}
	mac, err := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	e, err := New(Config{
		Clock: fc, Proxmox: tracked, Store: st, Logger: discardLogger(), DryRun: false, Loc: loc,
		Guests: []GuestConfig{
			{Name: "lxc-app", Schedule: "daytime", DependsOn: []string{"lxc-base"}},
			{Name: "lxc-base", Schedule: "daytime"},
		},
		Schedules:      schedules,
		Host:           HostConfig{Schedule: "alwayson"},
		WoLSender:      &fakeWoLSender{},
		WoLMAC:         mac,
		WoLRetries:     3,
		WoLWakeTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if err := e.Tick(ctx); err != nil { // bootstrap, both off, no-op
		t.Fatalf("bootstrap tick: %v", err)
	}

	fc.Set(time.Date(2026, 1, 5, 7, 0, 0, 0, loc)) // on-boundary: both start
	if err := e.Tick(ctx); err != nil {
		t.Fatalf("tick at on-boundary: %v", err)
	}
	assertStringSlices(t, order, []string{"start:100", "start:101"}) // base before app

	fc.Set(time.Date(2026, 1, 5, 19, 0, 0, 0, loc)) // off-boundary: both stop
	if err := e.Tick(ctx); err != nil {
		t.Fatalf("tick at off-boundary: %v", err)
	}
	assertStringSlices(t, order, []string{"start:100", "start:101", "shutdown:101", "shutdown:100"}) // app before base
}

func TestAlwaysOnGuestNeverTouched(t *testing.T) {
	loc := time.UTC
	fc := clock.NewFake(time.Date(2026, 1, 5, 7, 0, 0, 0, loc))
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 100, Name: "lxc-forge", Kind: proxmox.KindLXC, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "lxc-forge", AlwaysOn: true}},
		map[string]schedule.Schedule{}, fc, fp, st, false, loc)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := te.Tick(ctx); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		fc.Advance(time.Hour)
	}
	if g, _ := fp.Guest(100); g.Status != proxmox.StatusStopped {
		t.Fatal("expected an always_on guest to never be started by the engine")
	}
	if got := eventKinds(t, st); len(got) != 0 {
		t.Fatalf("expected no events for an always_on guest, got %v", got)
	}
}

func TestUntrustedClockSkipsTick(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})
	fc := clock.NewFake(time.Date(2026, 1, 5, 7, 0, 0, 0, loc))
	fc.SetTrusted(false)
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime}, fc, fp, st, false, loc)
	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatal("expected the engine to take no action while the clock is untrusted")
	}
	if _, ok, _ := st.GetState(context.Background(), "last_tick"); ok {
		t.Fatal("expected last_tick to remain unset while the clock is untrusted")
	}
}

func TestDryRunNeverCallsProxmoxMutatingMethods(t *testing.T) {
	loc := time.UTC
	daytime := mustSchedule(t, [3]string{"mon-fri", "07:00", "19:00"})
	fc := clock.NewFake(time.Date(2026, 1, 5, 7, 0, 0, 0, loc)) // on-boundary
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-work", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})
	st := openTestStore(t)

	te := newTestEngine(t, []GuestConfig{{Name: "vm-work", Schedule: "daytime"}},
		map[string]schedule.Schedule{"daytime": daytime}, fc, fp, st, true /* dry-run */, loc)
	if err := te.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if g, _ := fp.Guest(201); g.Status != proxmox.StatusStopped {
		t.Fatal("dry-run must not actually start the guest")
	}
	assertStringSlices(t, eventKinds(t, st), []string{"guest_start:vm-work"})
}

func TestDependencyCycleRejectedAtConstruction(t *testing.T) {
	_, err := New(Config{
		Clock: clock.NewFake(time.Now()), Proxmox: proxmox.NewFake(), Logger: discardLogger(), Loc: time.UTC,
		Guests: []GuestConfig{
			{Name: "a", Schedule: "s", DependsOn: []string{"b"}},
			{Name: "b", Schedule: "s", DependsOn: []string{"a"}},
		},
		Schedules: map[string]schedule.Schedule{"s": nil},
	})
	if err == nil {
		t.Fatal("expected an error for a dependency cycle")
	}
}

func assertStringSlices(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q\n full got:  %v\n full want: %v", i, got[i], want[i], got, want)
		}
	}
}
