// Package notify sends alerts through a service outside the homelab (per
// CLAUDE.md: labpower must not depend on anything running on the Proxmox
// host, including notifications, since it has to work while the host is
// off).
package notify

import "context"

// Priority is how urgently a notification should be delivered.
type Priority int

const (
	PriorityDefault Priority = iota
	PriorityHigh
	PriorityUrgent
)

// Notification is one alert.
type Notification struct {
	Title    string
	Body     string
	Priority Priority
}

// Notifier delivers notifications. Implementations must be safe for
// concurrent use.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
