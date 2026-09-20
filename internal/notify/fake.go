package notify

import (
	"context"
	"sync"
)

// Fake records notifications for tests instead of sending them anywhere.
type Fake struct {
	mu   sync.Mutex
	sent []Notification
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Notify(_ context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return nil
}

// Sent returns every notification recorded so far.
func (f *Fake) Sent() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Notification, len(f.sent))
	copy(out, f.sent)
	return out
}

var _ Notifier = (*Fake)(nil)
