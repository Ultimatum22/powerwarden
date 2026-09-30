package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeAlwaysOnTestConfig is like writeTestConfig but vm-media's schedule
// covers the entire day (on=00:00, off=00:00 wraps to the following
// midnight), so its desired state is deterministic regardless of what time
// of day the test actually runs.
func writeAlwaysOnTestConfig(t *testing.T, srv *httptest.Server, dryRun bool) string {
	t.Helper()
	dir := t.TempDir()

	secretPath := filepath.Join(dir, "proxmox-token")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	yaml := `
dry_run: ` + boolStr(dryRun) + `
timezone: Europe/Amsterdam
listen: 127.0.0.1:0
public_url: https://power.example.com
trusted_proxy: 127.0.0.1

proxmox:
  url: ` + srv.URL + `
  node: pve01
  token_id: labpower@pve!pi
  token_secret_file: ` + secretPath + `
  tls_fingerprint: "` + fingerprintOf(srv) + `"

wol:
  method: unicast
  mac: "aa:bb:cc:dd:ee:ff"
  target: 10.22.10.250
  retries: 3
  wake_timeout: 5m

schedules:
  daytime:
    - { days: mon-fri, on: "07:00", off: "01:00" }
  alwayson:
    - { days: mon-sun, on: "00:00", off: "00:00" }

host:
  schedule: alwayson
  shutdown_grace: 10m

guests:
  vm-media: { schedule: alwayson, depends_on: [] }

notify:
  provider: ntfy
  url: https://ntfy.sh/labpower-test

clock:
  trust: system # tests must not depend on the machine's time sync

auth:
  rp_id: power.example.com
  session_idle: 30m
  session_absolute: 12h
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// serveRun is a `labpower serve` running in the background. Tests stop it
// once what they're waiting for has happened instead of giving it a fixed
// deadline: on a cold CI runner under -race, startup alone (SQLite open +
// migrations) can take longer than any short deadline.
type serveRun struct {
	cancel context.CancelFunc
	done   chan error
}

func startServe(args []string, logger *slog.Logger) *serveRun {
	ctx, cancel := context.WithCancel(context.Background())
	r := &serveRun{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- run(ctx, args, logger) }()
	return r
}

// waitFor blocks until ch is closed or receives, failing the test if serve
// exits first or nothing happens within serveWaitTimeout.
func (r *serveRun) waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case err := <-r.done:
		t.Fatalf("serve exited before %s: %v", what, err)
	case <-time.After(serveWaitTimeout):
		r.cancel()
		t.Fatalf("timed out after %s waiting for %s", serveWaitTimeout, what)
	}
}

// stop cancels serve and returns its result.
func (r *serveRun) stop() error {
	r.cancel()
	return <-r.done
}

const serveWaitTimeout = 30 * time.Second

// signalHandler closes ch the first time a record with message msg is
// logged, so a test can wait for a point in serve's startup.
type signalHandler struct {
	slog.Handler
	msg  string
	once *sync.Once
	ch   chan struct{}
}

func loggerSignalling(msg string) (*slog.Logger, <-chan struct{}) {
	ch := make(chan struct{})
	h := signalHandler{Handler: testLogger().Handler(), msg: msg, once: &sync.Once{}, ch: ch}
	return slog.New(h), ch
}

func (h signalHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.once.Do(func() { close(h.ch) })
	}
	return h.Handler.Handle(ctx, r)
}

func (h signalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func (h signalHandler) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}

func TestServeTicksAndShutsDownCleanly(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true) // dry-run: never touches the mutating endpoints
	stateDir := t.TempDir()

	logger, started := loggerSignalling("labpower serve starting")
	r := startServe([]string{"serve", "-config", cfgPath, "-state-dir", stateDir}, logger)
	r.waitFor(t, started, "serve to start")
	if err := r.stop(); err != nil {
		t.Fatalf("run serve: %v", err)
	}

	if _, err := os.Stat(filepath.Join(stateDir, "labpower.db")); err != nil {
		t.Fatalf("expected labpower.db to be created in state-dir: %v", err)
	}
}

func TestServeLiveModeStartsGuestForReal(t *testing.T) {
	started := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/pve01/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"uptime": 1}})
	})
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "stopped"},
			},
		})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/start", func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		writeJSON(w, map[string]any{"data": "UPID:pve01:live-start"})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/tasks/UPID:pve01:live-start/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"status": "stopped", "exitstatus": "OK"}})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	cfgPath := writeAlwaysOnTestConfig(t, srv, false) // dry_run: false — this is milestone 3's whole point
	stateDir := t.TempDir()

	r := startServe([]string{"serve", "-config", cfgPath, "-state-dir", stateDir}, testLogger())
	r.waitFor(t, started, "live serve to call the real (fake) Proxmox start endpoint")
	if err := r.stop(); err != nil {
		t.Fatalf("run serve: %v", err)
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(badPath, []byte("dry_run: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := run(ctx, []string{"serve", "-config", badPath, "-state-dir", dir}, testLogger()); err == nil {
		t.Fatal("expected serve to reject an invalid config before starting the loop")
	}
}
