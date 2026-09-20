package proxmox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"context"
)

// fakeProxmoxServer stands in for a real Proxmox node, per CLAUDE.md's rule
// against calling a real Proxmox API in tests.
func fakeProxmoxServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("type"); got != "vm" {
			t.Errorf("cluster/resources called with type=%q, want vm", got)
		}
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "running"},
				{"id": "lxc/100", "type": "lxc", "vmid": 100, "name": "lxc-forge", "node": "pve01", "status": "stopped"},
				{"id": "storage/local", "type": "storage", "node": "pve01"},
			},
		})
	})

	mux.HandleFunc("/api2/json/nodes/pve01/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"data": map[string]any{"uptime": 12345}})
			return
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.PostForm.Get("command"); got != "shutdown" {
				t.Errorf("node status POST command=%q, want shutdown", got)
			}
			writeJSON(w, map[string]any{"data": "UPID:pve01:shutdown"})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST for guest start, got %s", r.Method)
		}
		writeJSON(w, map[string]any{"data": "UPID:pve01:qemu-start"})
	})

	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST for guest shutdown, got %s", r.Method)
		}
		writeJSON(w, map[string]any{"data": "UPID:pve01:qemu-shutdown"})
	})

	mux.HandleFunc("/api2/json/nodes/pve01/tasks", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("source"); got != "active" {
			t.Errorf("tasks called with source=%q, want active", got)
		}
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"upid": "UPID:pve01:backup", "type": "vzdump", "user": "root@pam"},
			},
		})
	})

	mux.HandleFunc("/api2/json/nodes/pve01/tasks/UPID:pve01:qemu-start/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"status": "stopped", "exitstatus": "OK"}})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
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

func newTestClient(t *testing.T, srv *httptest.Server) *HTTPClient {
	t.Helper()
	c, err := NewHTTPClient(srv.URL, "pve01", "labpower@pve!pi", "secret", fingerprintOf(srv))
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	return c
}

func TestListGuestsFiltersToVMsAndParsesFields(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	guests, err := c.ListGuests(context.Background())
	if err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	if len(guests) != 2 {
		t.Fatalf("got %d guests, want 2 (storage resource must be filtered out): %+v", len(guests), guests)
	}
	byName := map[string]Guest{}
	for _, g := range guests {
		byName[g.Name] = g
	}
	vmMedia, ok := byName["vm-media"]
	if !ok {
		t.Fatal("vm-media not found")
	}
	if vmMedia.VMID != 201 || vmMedia.Kind != KindQEMU || vmMedia.Status != StatusRunning {
		t.Errorf("vm-media = %+v, unexpected fields", vmMedia)
	}
	lxcForge, ok := byName["lxc-forge"]
	if !ok {
		t.Fatal("lxc-forge not found")
	}
	if lxcForge.VMID != 100 || lxcForge.Kind != KindLXC || lxcForge.Status != StatusStopped {
		t.Errorf("lxc-forge = %+v, unexpected fields", lxcForge)
	}
}

func TestNodeStatus(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	st, err := c.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus: %v", err)
	}
	if st.Uptime != 12345 {
		t.Errorf("Uptime = %d, want 12345", st.Uptime)
	}
}

func TestStartAndShutdownGuestUseCorrectVerbsAndPaths(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	upid, err := c.StartGuest(context.Background(), KindQEMU, 201)
	if err != nil {
		t.Fatalf("StartGuest: %v", err)
	}
	if upid != "UPID:pve01:qemu-start" {
		t.Errorf("upid = %q", upid)
	}

	upid, err = c.ShutdownGuest(context.Background(), KindQEMU, 201)
	if err != nil {
		t.Fatalf("ShutdownGuest: %v", err)
	}
	if upid != "UPID:pve01:qemu-shutdown" {
		t.Errorf("upid = %q", upid)
	}
}

func TestTaskStatusParsesCompletedOK(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	ts, err := c.TaskStatus(context.Background(), "UPID:pve01:qemu-start")
	if err != nil {
		t.Fatalf("TaskStatus: %v", err)
	}
	if ts.State != TaskOK {
		t.Errorf("State = %q, want OK", ts.State)
	}
}

func TestActiveTasks(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	tasks, err := c.ActiveTasks(context.Background())
	if err != nil {
		t.Fatalf("ActiveTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Type != "vzdump" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestShutdownHostSendsCommandShutdown(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	upid, err := c.ShutdownHost(context.Background())
	if err != nil {
		t.Fatalf("ShutdownHost: %v", err)
	}
	if upid != "UPID:pve01:shutdown" {
		t.Errorf("upid = %q", upid)
	}
}

func TestAuthorizationHeaderFormat(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.ListGuests(context.Background()); err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	want := "PVEAPIToken=labpower@pve!pi=secret"
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestTLSFingerprintMismatchRejected(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()

	// Deliberately wrong fingerprint (all zeros).
	c, err := NewHTTPClient(srv.URL, "pve01", "id", "secret", strings.Repeat("00:", 31)+"00")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if _, err := c.ListGuests(context.Background()); err == nil {
		t.Fatal("expected TLS fingerprint mismatch error")
	}
}

func TestInvalidFingerprintRejectedAtConstruction(t *testing.T) {
	if _, err := NewHTTPClient("https://example.invalid", "pve01", "id", "secret", "not-hex"); err == nil {
		t.Fatal("expected error for invalid fingerprint")
	}
}
