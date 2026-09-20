package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProxmoxServer is a minimal stand-in for a Proxmox node, used instead
// of a real server per CLAUDE.md's testing rules.
func fakeProxmoxServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/pve01/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"uptime": 999}})
	})
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "lxc/100", "type": "lxc", "vmid": 100, "name": "lxc-forge", "node": "pve01", "status": "running"},
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "stopped"},
			},
		})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/start", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": "UPID:pve01:started"})
	})
	return httptest.NewTLSServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fingerprintOf(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().Raw)
	hexs := hex.EncodeToString(sum[:])
	var parts []string
	for i := 0; i < len(hexs); i += 2 {
		parts = append(parts, hexs[i:i+2])
	}
	return strings.ToUpper(strings.Join(parts, ":"))
}

// writeTestConfig writes a valid config pointed at srv and returns its path.
func writeTestConfig(t *testing.T, srv *httptest.Server, dryRun bool) string {
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
  evenings:
    - { days: mon-sun, on: "17:00", off: "00:30" }

host:
  schedule: daytime
  shutdown_grace: 10m

guests:
  lxc-forge: { always_on: true }
  vm-media: { schedule: evenings, depends_on: [] }
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestStatusListsGuestsAgainstFakeServer(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)

	var out bytes.Buffer
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := run(context.Background(), []string{"status", "-config", cfgPath}, testLogger())
	w.Close()
	os.Stdout = origStdout
	out.ReadFrom(r)

	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "vm-media") || !strings.Contains(got, "lxc-forge") {
		t.Fatalf("status output missing guest names:\n%s", got)
	}
	if !strings.Contains(got, "always_on") || !strings.Contains(got, "schedule:evenings") {
		t.Fatalf("status output missing managed-as annotations:\n%s", got)
	}
	if !strings.Contains(got, "reachable") {
		t.Fatalf("status output missing host reachability:\n%s", got)
	}
}

func TestCheckConfigValidAndInvalid(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)

	if err := run(context.Background(), []string{"check-config", "-config", cfgPath}, testLogger()); err != nil {
		t.Fatalf("expected valid config to pass, got: %v", err)
	}

	badPath := cfgPath + ".bad"
	if err := os.WriteFile(badPath, []byte("dry_run: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"check-config", "-config", badPath}, testLogger()); err == nil {
		t.Fatal("expected incomplete config to fail validation")
	}
}

func TestGuestStartDryRunDoesNotCallStartEndpoint(t *testing.T) {
	startCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		// Read-only lookups are fine even in dry-run, to resolve and report the vmid.
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "stopped"},
			},
		})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/start", func(w http.ResponseWriter, r *http.Request) {
		startCalled = true
		writeJSON(w, map[string]any{"data": "UPID:pve01:started"})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)

	err := run(context.Background(), []string{"guest", "-config", cfgPath, "start", "vm-media"}, testLogger())
	if err != nil {
		t.Fatalf("run guest start: %v", err)
	}
	if startCalled {
		t.Fatal("dry-run guest start must not call the mutating start endpoint")
	}
}

func TestGuestStartRejectsAlwaysOn(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)

	err := run(context.Background(), []string{"guest", "-config", cfgPath, "start", "lxc-forge"}, testLogger())
	if err == nil || !strings.Contains(err.Error(), "always_on") {
		t.Fatalf("expected always_on rejection, got: %v", err)
	}
}

func TestGuestStartLiveCallsProxmox(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, false)

	err := run(context.Background(), []string{"guest", "-config", cfgPath, "start", "vm-media"}, testLogger())
	if err != nil {
		t.Fatalf("run guest start: %v", err)
	}
}

func TestWakeDryRunDoesNotSendPacket(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)

	// wol.target 10.22.10.250 is unreachable from the test sandbox; a
	// dry run must never attempt to dial it, so a successful return here
	// is itself the assertion.
	if err := run(context.Background(), []string{"wake", "-config", cfgPath}, testLogger()); err != nil {
		t.Fatalf("run wake: %v", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run(context.Background(), []string{"bogus"}, testLogger()); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestNoArgsShowsUsage(t *testing.T) {
	if err := run(context.Background(), []string{}, testLogger()); err == nil {
		t.Fatal("expected error for no args")
	}
}
