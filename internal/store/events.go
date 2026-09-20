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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, kind, target, actor, reason, ip, dry_run
		FROM events ORDER BY at DESC, id DESC LIMIT ?`, limit)
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
