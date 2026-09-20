package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
listen: 127.0.0.1:8080
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
  schedule: daytime
  shutdown_grace: 10m

guests:
  vm-media: { schedule: alwayson, depends_on: [] }
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestServeTicksAndShutsDownCleanly(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true) // dry-run: never touches the mutating endpoints
	stateDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := run(ctx, []string{"serve", "-config", cfgPath, "-state-dir", stateDir}, testLogger())
	if err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := run(ctx, []string{"serve", "-config", cfgPath, "-state-dir", stateDir}, testLogger()); err != nil {
		t.Fatalf("run serve: %v", err)
	}

	select {
	case <-started:
	default:
		t.Fatal("expected live serve to call the real (fake) Proxmox start endpoint, but it never did")
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
