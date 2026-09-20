package store

import "time"

// Override is a manual on/off/pause/ignore_weather instruction that
// outranks the schedule for target ('host' or a guest name) until it's
// cancelled or Until passes.
type Override struct {
	ID          int64
	Target      string
	Action      string // on | off | pause | ignore_weather
	Until       *time.Time
	CreatedBy   string // user id, or "system"
	CreatedAt   time.Time
	CancelledAt *time.Time
}

// activeAt reports whether the override was in effect at instant t: created
// by then, not yet expired, and not cancelled.
func (o Override) activeAt(t time.Time) bool {
	if o.CreatedAt.After(t) {
		return false
	}
	if o.Until != nil && !o.Until.After(t) {
		return false
	}
	if o.CancelledAt != nil && !o.CancelledAt.After(t) {
		return false
	}
	return true
}

// Event is one row in the audit log.
type Event struct {
	ID     int64
	At     time.Time
	Kind   string
	Target string
	Actor  string // 'schedule' | 'weather' | 'user:<id>' | 'system'
	Reason string
	IP     string
	DryRun bool
}
