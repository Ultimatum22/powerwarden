package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostShutdownWithoutYesDoesNotShutDown(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, false)

	if err := run(context.Background(), []string{"host", "shutdown", "-config", cfgPath}, testLogger()); err != nil {
		t.Fatalf("run host shutdown: %v", err)
	}
}

func TestHostShutdownRefusedWithActiveTasks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]any{}})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"upid": "UPID:pve01:backup", "type": "vzdump", "user": "root@pam"}}})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, false)

	err := run(context.Background(), []string{"host", "shutdown", "-config", cfgPath, "-yes"}, testLogger())
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("expected refusal mentioning active tasks, got: %v", err)
	}
}

func TestHostShutdownDryRunDoesNotCallMutatingEndpoints(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "running"},
			},
		})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]any{}})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/shutdown", func(w http.ResponseWriter, r *http.Request) {
		called = true
		writeJSON(w, map[string]any{"data": "UPID:pve01:guest-shutdown"})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			called = true
		}
		writeJSON(w, map[string]any{"data": "UPID:pve01:host-shutdown"})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true) // dry_run: true

	if err := run(context.Background(), []string{"host", "shutdown", "-config", cfgPath, "-yes"}, testLogger()); err != nil {
		t.Fatalf("run host shutdown: %v", err)
	}
	if called {
		t.Fatal("dry-run host shutdown must not call any mutating endpoint")
	}
}

func TestHostShutdownLiveStopsGuestsThenHost(t *testing.T) {
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []map[string]any{
				{"id": "lxc/100", "type": "lxc", "vmid": 100, "name": "lxc-forge", "node": "pve01", "status": "running"},
				{"id": "qemu/201", "type": "qemu", "vmid": 201, "name": "vm-media", "node": "pve01", "status": "running"},
			},
		})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]any{}})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/qemu/201/status/shutdown", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "guest-shutdown:201")
		writeJSON(w, map[string]any{"data": "UPID:pve01:guest-shutdown"})
	})
	mux.HandleFunc("/api2/json/nodes/pve01/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			calls = append(calls, "host-shutdown")
			writeJSON(w, map[string]any{"data": "UPID:pve01:host-shutdown"})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"uptime": 1}})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, false) // dry_run: false

	if err := run(context.Background(), []string{"host", "shutdown", "-config", cfgPath, "-yes"}, testLogger()); err != nil {
		t.Fatalf("run host shutdown: %v", err)
	}

	// lxc-forge is always_on and must never be touched; vm-media must stop
	// before the host does.
	if len(calls) != 2 || calls[0] != "guest-shutdown:201" || calls[1] != "host-shutdown" {
		t.Fatalf("calls = %v, want [guest-shutdown:201 host-shutdown]", calls)
	}
}
