package web

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

var testGuestConfig = engine.GuestConfig{Name: "vm-media", Schedule: "daytime"}

var testHostConfig = engine.HostConfig{Schedule: "daytime"}

type testHarness struct {
	*Server
	Fake  *proxmox.Fake
	Clock *clock.Fake
}

func newTestServer(t *testing.T) *testHarness {
	t.Helper()

	path := filepath.Join(t.TempDir(), "labpower.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fc := clock.NewFake(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC))
	fp := proxmox.NewFake()
	fp.AddGuest(proxmox.Guest{VMID: 201, Name: "vm-media", Kind: proxmox.KindQEMU, Status: proxmox.StatusStopped})

	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := auth.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthn("localhost", "labpower test", []string{"https://localhost"})
	if err != nil {
		t.Fatal(err)
	}

	loc := time.UTC
	daytime := mustTestSchedule(t, "mon-fri", "07:00", "19:00")

	cfg := Config{
		Store:      st,
		Proxmox:    fp,
		Notifier:   notify.NewFake(),
		Clock:      fc,
		Sessions:   &auth.Sessions{Store: st, IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour, StepUpDuration: 5 * time.Minute, Secure: true},
		WebAuthn:   wa,
		Challenges: auth.NewChallengeStore(),
		SecretBox:  box,
		CSRFKey:    []byte("test-csrf-key-32-bytes-long!!!!!"),

		LoginLimiter:  auth.NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour),
		StepUpLimiter: auth.NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour),
		TrustedProxy:  "",
		RPID:          "localhost",

		Guests:    []engine.GuestConfig{testGuestConfig},
		Schedules: map[string]schedule.Schedule{"daytime": daytime},
		Host:      testHostConfig,
		Loc:       loc,
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &testHarness{Server: srv, Fake: fp, Clock: fc}
}

func mustTestSchedule(t *testing.T, days, on, off string) schedule.Schedule {
	t.Helper()
	w, err := schedule.NewWindow(days, on, off)
	if err != nil {
		t.Fatal(err)
	}
	return schedule.Schedule{w}
}

// createTestSession bypasses the WebAuthn ceremony (which needs a real
// authenticator to simulate) and creates a session directly, the same way
// a successful login would. Also creates the backing user row.
func (h *testHarness) createTestSession(t *testing.T) (rawToken string, tokenHash []byte) {
	t.Helper()
	ctx := context.Background()
	if err := h.Store.CreateUser(ctx, store.User{ID: store.SoleUserID, Name: "owner"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	raw, err := h.Sessions.Create(ctx, store.SoleUserID, "10.0.0.1", "test-agent", h.Clock.Now())
	if err != nil {
		t.Fatalf("Sessions.Create: %v", err)
	}
	return raw, auth.HashToken(raw)
}
