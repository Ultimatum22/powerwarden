package proxmox

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Client for tests. It never touches the network, per
// CLAUDE.md's rule against calling a real Proxmox API in tests.
type Fake struct {
	mu sync.Mutex

	guests      map[int]Guest
	activeTasks []Task
	nextUPID    int
	tasks       map[UPID]TaskStatus

	NodeReachable bool
	NodeUptime    int64

	// NodeStatusErr, if set, is returned by NodeStatus instead of a
	// result, to simulate the host being off/unreachable.
	NodeStatusErr error
}

// NewFake builds an empty Fake. Use AddGuest to populate it.
func NewFake() *Fake {
	return &Fake{
		guests:        make(map[int]Guest),
		tasks:         make(map[UPID]TaskStatus),
		NodeReachable: true,
	}
}

// AddGuest registers a guest as the fake cluster's inventory.
func (f *Fake) AddGuest(g Guest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guests[g.VMID] = g
}

// SetActiveTasks controls what ActiveTasks returns.
func (f *Fake) SetActiveTasks(tasks []Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeTasks = tasks
}

// Guest returns the current state of a guest, for assertions in tests.
func (f *Fake) Guest(vmid int) (Guest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.guests[vmid]
	return g, ok
}

func (f *Fake) ListGuests(_ context.Context) ([]Guest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Guest, 0, len(f.guests))
	for _, g := range f.guests {
		out = append(out, g)
	}
	return out, nil
}

func (f *Fake) NodeStatus(_ context.Context) (NodeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NodeStatusErr != nil {
		return NodeStatus{}, f.NodeStatusErr
	}
	if !f.NodeReachable {
		return NodeStatus{}, fmt.Errorf("proxmox: fake node unreachable")
	}
	return NodeStatus{Uptime: f.NodeUptime}, nil
}

func (f *Fake) StartGuest(_ context.Context, kind GuestKind, vmid int) (UPID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.guests[vmid]
	if !ok {
		return "", fmt.Errorf("proxmox: fake: no such guest %d", vmid)
	}
	if g.Kind != kind {
		return "", fmt.Errorf("proxmox: fake: guest %d is %s, not %s", vmid, g.Kind, kind)
	}
	g.Status = StatusRunning
	f.guests[vmid] = g
	return f.completedTask(), nil
}

func (f *Fake) ShutdownGuest(_ context.Context, kind GuestKind, vmid int) (UPID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.guests[vmid]
	if !ok {
		return "", fmt.Errorf("proxmox: fake: no such guest %d", vmid)
	}
	if g.Kind != kind {
		return "", fmt.Errorf("proxmox: fake: guest %d is %s, not %s", vmid, g.Kind, kind)
	}
	g.Status = StatusStopped
	f.guests[vmid] = g
	return f.completedTask(), nil
}

func (f *Fake) TaskStatus(_ context.Context, upid UPID) (TaskStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok := f.tasks[upid]
	if !ok {
		return TaskStatus{}, fmt.Errorf("proxmox: fake: no such task %q", upid)
	}
	return ts, nil
}

func (f *Fake) ActiveTasks(_ context.Context) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Task, len(f.activeTasks))
	copy(out, f.activeTasks)
	return out, nil
}

func (f *Fake) ShutdownHost(_ context.Context) (UPID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NodeReachable = false
	return f.completedTask(), nil
}

// completedTask allocates a new UPID already marked OK. Callers must hold f.mu.
func (f *Fake) completedTask() UPID {
	f.nextUPID++
	upid := UPID(fmt.Sprintf("UPID:fake:%d", f.nextUPID))
	f.tasks[upid] = TaskStatus{State: TaskOK, ExitStatus: "OK"}
	return upid
}

var _ Client = (*Fake)(nil)
