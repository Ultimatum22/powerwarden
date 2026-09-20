// Package proxmox is a thin client for the subset of the Proxmox VE API
// labpower needs: listing guests, starting/stopping them, checking the
// node and active tasks, and shutting the node down. It intentionally does
// not wrap the whole API surface — see CLAUDE.md ("Proxmox client ... are
// hand-written (small)").
package proxmox

import "context"

// GuestKind distinguishes QEMU VMs from LXC containers, since they live
// under different API paths.
type GuestKind string

const (
	KindQEMU GuestKind = "qemu"
	KindLXC  GuestKind = "lxc"
)

// GuestStatus is a guest's run state as reported by Proxmox.
type GuestStatus string

const (
	StatusRunning GuestStatus = "running"
	StatusStopped GuestStatus = "stopped"
)

// Guest is one VM or LXC container as returned by GET /cluster/resources.
type Guest struct {
	VMID   int
	Name   string
	Kind   GuestKind
	Node   string
	Status GuestStatus
}

// NodeStatus reports whether the Proxmox node responded.
type NodeStatus struct {
	Node   string
	Uptime int64 // seconds
}

// UPID is a Proxmox task identifier returned by any action that starts an
// asynchronous task (guest start/shutdown, node shutdown).
type UPID string

// TaskState is a task's terminal or in-progress state.
type TaskState string

const (
	TaskRunning TaskState = "running"
	TaskOK      TaskState = "OK"
	TaskError   TaskState = "error"
)

// Task is one entry from GET /nodes/{node}/tasks?source=active.
type Task struct {
	UPID UPID
	Type string
	User string
}

// TaskStatus is the result of GET /nodes/{node}/tasks/{upid}/status.
type TaskStatus struct {
	State      TaskState
	ExitStatus string
}

// Client is the Proxmox operations labpower needs. All implementations
// must be safe for concurrent use.
type Client interface {
	// ListGuests returns every VM/LXC on the cluster (GET /cluster/resources?type=vm).
	ListGuests(ctx context.Context) ([]Guest, error)

	// NodeStatus reports whether the configured node is reachable
	// (GET /nodes/{node}/status). A network error means the host is off
	// or unreachable, not necessarily a bug; callers should treat it as
	// "not on" rather than a fatal error.
	NodeStatus(ctx context.Context) (NodeStatus, error)

	// StartGuest starts a guest (POST .../status/start) and returns the task ID.
	StartGuest(ctx context.Context, kind GuestKind, vmid int) (UPID, error)

	// ShutdownGuest requests a clean shutdown (POST .../status/shutdown,
	// never "stop") and returns the task ID.
	ShutdownGuest(ctx context.Context, kind GuestKind, vmid int) (UPID, error)

	// TaskStatus polls a task by UPID (GET /nodes/{node}/tasks/{upid}/status).
	TaskStatus(ctx context.Context, upid UPID) (TaskStatus, error)

	// ActiveTasks lists tasks currently running on the node
	// (GET /nodes/{node}/tasks?source=active), used to postpone host
	// shutdown while a backup or other job is in flight.
	ActiveTasks(ctx context.Context) ([]Task, error)

	// ShutdownHost requests a clean node shutdown
	// (POST /nodes/{node}/status, command=shutdown) and returns the task ID.
	ShutdownHost(ctx context.Context) (UPID, error)
}
