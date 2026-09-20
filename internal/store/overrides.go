package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateOverride inserts a new override and returns its ID.
func (s *Store) CreateOverride(ctx context.Context, o Override) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO overrides (target, action, until, created_by, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		o.Target, o.Action, unixPtr(o.Until), o.CreatedBy, o.CreatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: create override: %w", err)
	}
	return res.LastInsertId()
}

// CancelOverride marks an override cancelled as of now, so it stops
// applying immediately (used by the future "Cancel" button in the UI).
func (s *Store) CancelOverride(ctx context.Context, id int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE overrides SET cancelled_at = ? WHERE id = ? AND cancelled_at IS NULL`,
		now.Unix(), id)
	if err != nil {
		return fmt.Errorf("store: cancel override %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: cancel override %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: override %d not found or already cancelled", id)
	}
	return nil
}

// EffectiveOverride returns the override in effect for target at instant t
// (the most recently created one that was active then), or nil if none
// applies. Because "active at t" is computed from created_at/until/
// cancelled_at rather than any mutable "current" flag, this works equally
// well for t in the past (used by the engine to detect an override change
// since its last tick) or now.
func (s *Store) EffectiveOverride(ctx context.Context, target string, t time.Time) (*Override, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, target, action, until, created_by, created_at, cancelled_at
		FROM overrides
		WHERE target = ? AND created_at <= ?
		ORDER BY created_at DESC, id DESC
		LIMIT 50`, target, t.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: effective override for %q: %w", target, err)
	}
	defer rows.Close()

	for rows.Next() {
		o, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		if o.activeAt(t) {
			return &o, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: effective override for %q: %w", target, err)
	}
	return nil, nil
}

func scanOverride(rows *sql.Rows) (Override, error) {
	var o Override
	var until, cancelledAt sql.NullInt64
	var createdAt int64
	if err := rows.Scan(&o.ID, &o.Target, &o.Action, &until, &o.CreatedBy, &createdAt, &cancelledAt); err != nil {
		return Override{}, fmt.Errorf("store: scan override: %w", err)
	}
	o.CreatedAt = time.Unix(createdAt, 0).UTC()
	if until.Valid {
		t := time.Unix(until.Int64, 0).UTC()
		o.Until = &t
	}
	if cancelledAt.Valid {
		t := time.Unix(cancelledAt.Int64, 0).UTC()
		o.CancelledAt = &t
	}
	return o, nil
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}
