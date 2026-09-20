package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetState returns the value stored under key, and false if it's unset
// (e.g. last_tick before the engine's first run).
func (s *Store) GetState(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM state WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get state %q: %w", key, err)
	}
	return value, true, nil
}

// SetState upserts key=value.
func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set state %q: %w", key, err)
	}
	return nil
}
