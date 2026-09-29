package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RecordEvent appends one row to the audit log.
func (s *Store) RecordEvent(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (at, kind, target, actor, reason, ip, dry_run)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.At.Unix(), e.Kind, nullIfEmpty(e.Target), e.Actor, nullIfEmpty(e.Reason), nullIfEmpty(e.IP), boolToInt(e.DryRun))
	if err != nil {
		return fmt.Errorf("store: record event %q: %w", e.Kind, err)
	}
	return nil
}

// ListRecentEvents returns up to limit events, newest first.
func (s *Store) ListRecentEvents(ctx context.Context, limit int) ([]Event, error) {
	return s.ListEvents(ctx, EventQuery{Limit: limit})
}

// EventQuery filters and pages ListEvents. Paging is keyset-based: pass
// the last event of the previous page as Before.
type EventQuery struct {
	Kind   string // empty: all kinds
	Before *Event // only events strictly older than this one
	Limit  int
}

// ListEvents returns events matching q, newest first.
func (s *Store) ListEvents(ctx context.Context, q EventQuery) ([]Event, error) {
	where := "1=1"
	var args []any
	if q.Kind != "" {
		where += " AND kind = ?"
		args = append(args, q.Kind)
	}
	if q.Before != nil {
		at := q.Before.At.Unix()
		where += " AND (at < ? OR (at = ? AND id < ?))"
		args = append(args, at, at, q.Before.ID)
	}
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, kind, target, actor, reason, ip, dry_run
		FROM events WHERE `+where+` ORDER BY at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		var target, reason, ip sql.NullString
		var dryRun int
		if err := rows.Scan(&e.ID, &at, &e.Kind, &target, &e.Actor, &reason, &ip, &dryRun); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		e.At = time.Unix(at, 0).UTC()
		e.Target = target.String
		e.Reason = reason.String
		e.IP = ip.String
		e.DryRun = dryRun != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return out, nil
}

// GetEvent returns one event by ID.
func (s *Store) GetEvent(ctx context.Context, id int64) (Event, error) {
	var e Event
	var at int64
	var target, reason, ip sql.NullString
	var dryRun int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, at, kind, target, actor, reason, ip, dry_run FROM events WHERE id = ?`, id).
		Scan(&e.ID, &at, &e.Kind, &target, &e.Actor, &reason, &ip, &dryRun)
	if err != nil {
		return Event{}, fmt.Errorf("store: get event %d: %w", id, err)
	}
	e.At = time.Unix(at, 0).UTC()
	e.Target, e.Reason, e.IP, e.DryRun = target.String, reason.String, ip.String, dryRun != 0
	return e, nil
}

// EventKinds returns every distinct event kind present, sorted.
func (s *Store) EventKinds(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT kind FROM events ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("store: event kinds: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("store: event kinds: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// CountEventsSince counts events of kind at or after since.
func (s *Store) CountEventsSince(ctx context.Context, kind string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = ? AND at >= ?`, kind, since.Unix()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}

// HasEvent reports whether any event of kind by actor from ip exists
// (within the retention window, since older events are pruned).
func (s *Store) HasEvent(ctx context.Context, kind, actor, ip string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM events WHERE kind = ? AND actor = ? AND ip = ?)`, kind, actor, ip).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: has event: %w", err)
	}
	return n == 1, nil
}

// PruneEventsOlderThan deletes events older than cutoff (CLAUDE.md: prune
// events older than 90 days daily).
func (s *Store) PruneEventsOlderThan(ctx context.Context, cutoff time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, cutoff.Unix()); err != nil {
		return fmt.Errorf("store: prune events: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
