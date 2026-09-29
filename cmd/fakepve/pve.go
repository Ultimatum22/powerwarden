package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// guest is one simulated VM/LXC.
type guest struct {
	VMID   int
	Name   string
	Kind   string // "qemu" | "lxc"
	Status string // "running" | "stopped"
	OnBoot bool   // started automatically when the host boots
}

// task is one simulated Proxmox task (UPID). It finishes after its
// duration, applying done (if any) at that moment.
type task struct {
	UPID     string
	Type     string
	User     string
	finishAt time.Time
	done     func()
}

// PVE is an in-memory Proxmox node: just enough state and behaviour for
// labpower's client (guests, async tasks, node shutdown, Wake-on-LAN).
type PVE struct {
	Node      string
	Auth      string // expected Authorization header; empty accepts anything
	MAC       net.HardwareAddr
	TaskTime  time.Duration // how long guest start/shutdown tasks run
	BootTime  time.Duration // WoL packet -> API reachable
	HaltTime  time.Duration // node shutdown -> API unreachable
	Logger    *slog.Logger
	now       func() time.Time
	afterFunc func(time.Duration, func()) // time.AfterFunc, overridable in tests

	mu         sync.Mutex
	hostUp     bool
	guests     []*guest
	tasks      []*task
	seq        int
	scrubUntil time.Time // pool "tank" scrubs until then
}

// NewPVE returns a powered-on node with the demo guests.
func NewPVE(node, auth string, mac net.HardwareAddr, logger *slog.Logger) *PVE {
	return &PVE{
		Node:     node,
		Auth:     auth,
		MAC:      mac,
		TaskTime: 3 * time.Second,
		BootTime: 8 * time.Second,
		HaltTime: 2 * time.Second,
		Logger:   logger,
		hostUp:   true,
		guests: []*guest{
			{VMID: 100, Name: "lxc-forge", Kind: "lxc", Status: "running", OnBoot: true},
			{VMID: 101, Name: "lxc-edge", Kind: "lxc", Status: "running", OnBoot: true},
			{VMID: 201, Name: "vm-media", Kind: "qemu", Status: "stopped"},
			{VMID: 202, Name: "lxc-arr", Kind: "lxc", Status: "stopped"},
			{VMID: 300, Name: "vm-scratch", Kind: "qemu", Status: "stopped"},
		},
	}
}

func (p *PVE) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *PVE) after(d time.Duration, f func()) {
	if p.afterFunc != nil {
		p.afterFunc(d, f)
		return
	}
	time.AfterFunc(d, f)
}

// reapLocked applies and removes finished tasks. Callers hold p.mu.
func (p *PVE) reapLocked() {
	now := p.clock()
	kept := p.tasks[:0]
	for _, t := range p.tasks {
		if now.Before(t.finishAt) {
			kept = append(kept, t)
			continue
		}
		if t.done != nil {
			t.done()
		}
	}
	p.tasks = kept
}

func (p *PVE) newTaskLocked(typ, id string, d time.Duration, done func()) string {
	p.seq++
	upid := fmt.Sprintf("UPID:%s:%08X:%s:%s:root@pam:", p.Node, p.seq, typ, id)
	p.tasks = append(p.tasks, &task{UPID: upid, Type: typ, User: "labpower@pve!pi", finishAt: p.clock().Add(d), done: done})
	return upid
}

func (p *PVE) findLocked(name string) *guest {
	for _, g := range p.guests {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// APIHandler serves the Proxmox API subset under /api2/json. While the
// host is "off" every request is aborted mid-connection, which is what
// labpower sees from a powered-down server.
func (p *PVE) APIHandler() http.Handler {
	mux := http.NewServeMux()
	nodes := "/api2/json/nodes/" + p.Node

	mux.HandleFunc("GET /api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		type res struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			VMID   int    `json:"vmid"`
			Name   string `json:"name"`
			Node   string `json:"node"`
			Status string `json:"status"`
		}
		p.mu.Lock()
		out := make([]res, 0, len(p.guests))
		for _, g := range p.guests {
			out = append(out, res{fmt.Sprintf("%s/%d", g.Kind, g.VMID), g.Kind, g.VMID, g.Name, p.Node, g.Status})
		}
		p.mu.Unlock()
		writeData(w, out)
	})
	mux.HandleFunc("GET "+nodes+"/status", func(w http.ResponseWriter, r *http.Request) {
		writeData(w, map[string]any{"uptime": 86400})
	})
	mux.HandleFunc("POST "+nodes+"/status", func(w http.ResponseWriter, r *http.Request) {
		if r.PostFormValue("command") != "shutdown" {
			http.Error(w, "only command=shutdown is simulated", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		upid := p.newTaskLocked("srvshutdown", "", 0, nil)
		p.mu.Unlock()
		p.Logger.Warn("node shutdown requested: host powers off", "in", p.HaltTime)
		p.after(p.HaltTime, func() { p.PowerOff("shutdown") })
		writeData(w, upid)
	})
	mux.HandleFunc("POST "+nodes+"/{kind}/{vmid}/status/{action}", func(w http.ResponseWriter, r *http.Request) {
		kind, action := r.PathValue("kind"), r.PathValue("action")
		vmid, err := strconv.Atoi(r.PathValue("vmid"))
		if err != nil || (kind != "qemu" && kind != "lxc") {
			http.NotFound(w, r)
			return
		}
		var status, typ string
		switch action {
		case "start":
			status, typ = "running", kind+"start"
		case "shutdown":
			status, typ = "stopped", kind+"shutdown"
		default:
			// labpower must only ever use start and shutdown (never stop).
			http.Error(w, "action not simulated: "+action, http.StatusNotImplemented)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		var g *guest
		for _, c := range p.guests {
			if c.VMID == vmid && c.Kind == kind {
				g = c
			}
		}
		if g == nil {
			http.Error(w, "no such guest", http.StatusInternalServerError)
			return
		}
		upid := p.newTaskLocked(typ, strconv.Itoa(vmid), p.TaskTime, func() { g.Status = status })
		p.Logger.Info("guest task started", "guest", g.Name, "action", action, "takes", p.TaskTime)
		writeData(w, upid)
	})
	mux.HandleFunc("GET "+nodes+"/disks/zfs", func(w http.ResponseWriter, r *http.Request) {
		writeData(w, []map[string]any{{"name": "tank"}})
	})
	mux.HandleFunc("GET "+nodes+"/disks/zfs/tank", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		scrubbing := p.clock().Before(p.scrubUntil)
		p.mu.Unlock()
		scan := "scrub repaired 0B in 00:10:12 with 0 errors"
		if scrubbing {
			scan = "scrub in progress since " + p.clock().Format(time.ANSIC)
		}
		writeData(w, map[string]any{"name": "tank", "scan": scan})
	})
	mux.HandleFunc("GET "+nodes+"/tasks", func(w http.ResponseWriter, r *http.Request) {
		type res struct {
			UPID string `json:"upid"`
			Type string `json:"type"`
			User string `json:"user"`
		}
		p.mu.Lock()
		p.reapLocked()
		out := make([]res, 0, len(p.tasks))
		for _, t := range p.tasks {
			out = append(out, res{t.UPID, t.Type, t.User})
		}
		p.mu.Unlock()
		writeData(w, out)
	})
	mux.HandleFunc("GET "+nodes+"/tasks/{upid}/status", func(w http.ResponseWriter, r *http.Request) {
		upid := r.PathValue("upid")
		p.mu.Lock()
		p.reapLocked()
		running := false
		for _, t := range p.tasks {
			running = running || t.UPID == upid
		}
		p.mu.Unlock()
		if running {
			writeData(w, map[string]any{"status": "running"})
			return
		}
		writeData(w, map[string]any{"status": "stopped", "exitstatus": "OK"})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		up := p.hostUp
		p.mu.Unlock()
		if !up {
			panic(http.ErrAbortHandler) // connection dropped, like a powered-off host
		}
		if p.Auth != "" && r.Header.Get("Authorization") != p.Auth {
			p.Logger.Warn("rejected request with wrong API token", "path", r.URL.Path)
			http.Error(w, "authentication failure", http.StatusUnauthorized)
			return
		}
		p.Logger.Debug("api", "method", r.Method, "path", r.URL.Path)
		mux.ServeHTTP(w, r)
	})
}

// PowerOff drops the host: every guest stops, pending tasks vanish.
func (p *PVE) PowerOff(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hostUp {
		return
	}
	p.hostUp = false
	p.tasks = nil
	for _, g := range p.guests {
		g.Status = "stopped"
	}
	p.Logger.Warn("host is OFF", "reason", reason)
}

// PowerOn boots the host after BootTime, starting on-boot guests.
func (p *PVE) PowerOn(reason string) {
	p.mu.Lock()
	up := p.hostUp
	p.mu.Unlock()
	if up {
		p.Logger.Info("power-on ignored: host already on", "reason", reason)
		return
	}
	p.Logger.Warn("host booting", "reason", reason, "reachable_in", p.BootTime)
	p.after(p.BootTime, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.hostUp {
			return
		}
		p.hostUp = true
		for _, g := range p.guests {
			if g.OnBoot {
				g.Status = "running"
			}
		}
		p.Logger.Warn("host is ON")
	})
}

// HandleWoL checks a UDP payload for a magic packet addressed to p.MAC.
func (p *PVE) HandleWoL(payload []byte) {
	want := make([]byte, 0, 102)
	want = append(want, bytes.Repeat([]byte{0xff}, 6)...)
	for range 16 {
		want = append(want, p.MAC...)
	}
	if !bytes.Contains(payload, want) {
		p.Logger.Info("ignored UDP packet: not a magic packet for this host", "bytes", len(payload))
		return
	}
	p.PowerOn("Wake-on-LAN")
}

// SideHandler serves the plain-HTTP helper endpoints on loopback: an ntfy
// sink that logs notifications, and controls to simulate things that
// happen outside labpower (a start from the Proxmox UI, a backup job, a
// power cut).
func (p *PVE) SideHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, p.statusText())
		fmt.Fprint(w, `
controls (POST):
  /control/guests/{name}/start|stop   as if done in the Proxmox UI
  /control/backup?for=2m              an active task (postpones host shutdown)
  /control/scrub?for=10m              a ZFS scrub (fails the shutdown pre-check)
  /control/host/off|on                power cut / power button
`)
	})
	mux.HandleFunc("POST /ntfy/{topic}", func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(http.MaxBytesReader(w, r.Body, 64<<10))
		p.Logger.Info("NOTIFY", "topic", r.PathValue("topic"), "title", r.Header.Get("Title"),
			"priority", r.Header.Get("Priority"), "body", body.String())
		writeData(w, map[string]any{"event": "message"})
	})
	mux.HandleFunc("POST /control/guests/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		g := p.findLocked(r.PathValue("name"))
		if g == nil || !p.hostUp {
			http.Error(w, "no such guest, or host is off", http.StatusConflict)
			return
		}
		switch r.PathValue("action") {
		case "start":
			g.Status = "running"
		case "stop":
			g.Status = "stopped"
		default:
			http.NotFound(w, r)
			return
		}
		p.Logger.Info("control: guest changed outside labpower", "guest", g.Name, "status", g.Status)
		fmt.Fprintf(w, "%s is %s\n", g.Name, g.Status)
	})
	mux.HandleFunc("POST /control/backup", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("for"))
		if err != nil || d <= 0 {
			d = 2 * time.Minute
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.hostUp {
			http.Error(w, "host is off", http.StatusConflict)
			return
		}
		p.newTaskLocked("vzdump", "", d, nil)
		p.Logger.Info("control: backup task running", "for", d)
		fmt.Fprintf(w, "backup task running for %s\n", d)
	})
	mux.HandleFunc("POST /control/scrub", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("for"))
		if err != nil || d <= 0 {
			d = 10 * time.Minute
		}
		p.mu.Lock()
		p.scrubUntil = p.clock().Add(d)
		p.mu.Unlock()
		p.Logger.Info("control: ZFS scrub on tank", "for", d)
		fmt.Fprintf(w, "scrub running on tank for %s\n", d)
	})
	mux.HandleFunc("POST /control/host/{state}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("state") {
		case "off":
			p.PowerOff("control: power cut")
		case "on":
			p.PowerOn("control: power button")
		default:
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "ok\n")
	})
	return mux
}

func (p *PVE) statusText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reapLocked()
	var b strings.Builder
	state := "ON"
	if !p.hostUp {
		state = "OFF"
	}
	fmt.Fprintf(&b, "fake Proxmox node %s: %s\n\n", p.Node, state)
	gs := append([]*guest(nil), p.guests...)
	sort.Slice(gs, func(i, j int) bool { return gs[i].VMID < gs[j].VMID })
	for _, g := range gs {
		fmt.Fprintf(&b, "  %-4d %-5s %-12s %s\n", g.VMID, g.Kind, g.Name, g.Status)
	}
	fmt.Fprintf(&b, "\nactive tasks: %d\n", len(p.tasks))
	for _, t := range p.tasks {
		fmt.Fprintf(&b, "  %s (%s)\n", t.Type, t.UPID)
	}
	return b.String()
}

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}
