package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/wol"
)

// fakeTime is a manually advanced clock plus a queue of pending
// AfterFunc callbacks, so boot/halt/task delays run instantly in tests.
type fakeTime struct {
	mu      sync.Mutex
	now     time.Time
	pending []func()
}

func (f *fakeTime) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeTime) AfterFunc(_ time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, fn)
}

// advance moves time forward and fires every queued callback.
func (f *fakeTime) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	fns := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// newTestPVE serves a PVE's API over TLS and returns labpower's real
// client pointed at it, so this test also guards the fake against
// drifting from what the client expects.
func newTestPVE(t *testing.T) (*PVE, proxmox.Client, *fakeTime) {
	t.Helper()
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	ft := &fakeTime{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	p := NewPVE("pve01", "PVEAPIToken=labpower@pve!pi=secret", mac, slog.New(slog.DiscardHandler))
	p.now = ft.Now
	p.afterFunc = ft.AfterFunc

	srv := httptest.NewUnstartedServer(p.APIHandler())
	srv.Config.ErrorLog = slog.NewLogLogger(slog.DiscardHandler, slog.LevelError)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client, err := proxmox.NewHTTPClient(srv.URL, "pve01", "labpower@pve!pi", "secret", fingerprint(srv.Certificate().Raw))
	if err != nil {
		t.Fatal(err)
	}
	return p, client, ft
}

func guestStatus(t *testing.T, c proxmox.Client, name string) proxmox.GuestStatus {
	t.Helper()
	guests, err := c.ListGuests(context.Background())
	if err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	for _, g := range guests {
		if g.Name == name {
			return g.Status
		}
	}
	t.Fatalf("guest %s not listed", name)
	return ""
}

func TestGuestStartIsAnAsyncTask(t *testing.T) {
	_, c, ft := newTestPVE(t)
	ctx := context.Background()

	upid, err := c.StartGuest(ctx, proxmox.KindQEMU, 201)
	if err != nil {
		t.Fatalf("StartGuest: %v", err)
	}
	if st, _ := c.TaskStatus(ctx, upid); st.State != proxmox.TaskRunning {
		t.Fatalf("task state right after start = %q, want running", st.State)
	}
	if active, _ := c.ActiveTasks(ctx); len(active) != 1 {
		t.Fatalf("active tasks = %d, want 1", len(active))
	}
	if got := guestStatus(t, c, "vm-media"); got != proxmox.StatusStopped {
		t.Fatalf("vm-media before task finished = %q, want stopped", got)
	}

	ft.advance(time.Minute)
	if st, _ := c.TaskStatus(ctx, upid); st.State != proxmox.TaskOK {
		t.Fatalf("task state after finishing = %q, want OK", st.State)
	}
	if got := guestStatus(t, c, "vm-media"); got != proxmox.StatusRunning {
		t.Fatalf("vm-media after task = %q, want running", got)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	p, _, _ := newTestPVE(t)
	srv := httptest.NewTLSServer(p.APIHandler())
	defer srv.Close()
	bad, err := proxmox.NewHTTPClient(srv.URL, "pve01", "labpower@pve!pi", "wrong", fingerprint(srv.Certificate().Raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.ListGuests(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 with a wrong token, got %v", err)
	}
}

func TestShutdownThenWakeOnLAN(t *testing.T) {
	p, c, ft := newTestPVE(t)
	ctx := context.Background()

	if _, err := c.ShutdownHost(ctx); err != nil {
		t.Fatalf("ShutdownHost: %v", err)
	}
	ft.advance(time.Minute)
	if _, err := c.NodeStatus(ctx); err == nil {
		t.Fatal("NodeStatus succeeded while the host is off")
	}

	// A packet for another MAC does nothing.
	other, _ := net.ParseMAC("11:22:33:44:55:66")
	pkt, _ := wol.MagicPacket(other)
	p.HandleWoL(pkt)
	ft.advance(time.Minute)
	if _, err := c.NodeStatus(ctx); err == nil {
		t.Fatal("host woke on a magic packet for a different MAC")
	}

	pkt, _ = wol.MagicPacket(p.MAC)
	p.HandleWoL(pkt)
	ft.advance(time.Minute)
	if _, err := c.NodeStatus(ctx); err != nil {
		t.Fatalf("NodeStatus after WoL: %v", err)
	}
	if got := guestStatus(t, c, "lxc-forge"); got != proxmox.StatusRunning {
		t.Fatalf("on-boot guest lxc-forge = %q, want running", got)
	}
	if got := guestStatus(t, c, "vm-media"); got != proxmox.StatusStopped {
		t.Fatalf("scheduled guest vm-media = %q after boot, want stopped", got)
	}
}

func TestSideControlsAndNtfySink(t *testing.T) {
	p, c, _ := newTestPVE(t)
	side := httptest.NewServer(p.SideHandler())
	defer side.Close()

	post := func(path string) int {
		resp, err := http.Post(side.URL+path, "text/plain", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("/control/guests/vm-media/start"); code != http.StatusOK {
		t.Fatalf("control start = %d", code)
	}
	if got := guestStatus(t, c, "vm-media"); got != proxmox.StatusRunning {
		t.Fatalf("vm-media after control start = %q", got)
	}
	if code := post("/control/backup?for=1h"); code != http.StatusOK {
		t.Fatalf("control backup = %d", code)
	}
	if active, _ := c.ActiveTasks(context.Background()); len(active) != 1 || active[0].Type != "vzdump" {
		t.Fatalf("active tasks after backup = %+v", active)
	}
	if code := post("/ntfy/labpower-dev"); code != http.StatusOK {
		t.Fatalf("ntfy sink = %d", code)
	}
	if code := post("/control/host/off"); code != http.StatusOK {
		t.Fatalf("control host off = %d", code)
	}
	if _, err := c.NodeStatus(context.Background()); err == nil {
		t.Fatal("NodeStatus succeeded after a simulated power cut")
	}
}

func TestRequireLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8006": true,
		"localhost:8006": true,
		"[::1]:8006":     true,
		"0.0.0.0:8006":   false,
		"10.0.0.5:8006":  false,
	} {
		if err := requireLoopback(addr); (err == nil) != ok {
			t.Errorf("requireLoopback(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}

func TestScrubControlShowsInProgress(t *testing.T) {
	p, c, ft := newTestPVE(t)
	side := httptest.NewServer(p.SideHandler())
	defer side.Close()
	resp, err := http.Post(side.URL+"/control/scrub?for=10m", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got, err := c.ZFSScrubsInProgress(context.Background()); err != nil || len(got) != 1 || got[0] != "tank" {
		t.Fatalf("during scrub: %v, %v", got, err)
	}
	ft.advance(11 * time.Minute)
	if got, _ := c.ZFSScrubsInProgress(context.Background()); len(got) != 0 {
		t.Fatalf("after scrub: %v", got)
	}
}
