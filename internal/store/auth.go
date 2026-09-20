package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SoleUserID is the fixed user ID enrolment always uses. labpower is a
// single-owner appliance — CLAUDE.md's login screen has no account
// selector — so there is never more than one row in the users table, and
// callers that need "the" user (e.g. TOTP login, which has no username
// field to identify who's logging in) look it up by this constant.
const SoleUserID = "owner"

// User is one account. There is no password-only login path (CLAUDE.md:
// "No password-only login"); PasswordHash exists in the schema for
// completeness but is never set or checked by this milestone's auth flow,
// which is passkey-primary with TOTP backup.
type User struct {
	ID            string
	Name          string
	PasswordHash  string
	TOTPSecretEnc []byte
}

func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, name, password_hash, totp_secret_enc) VALUES (?, ?, ?, ?)`,
		u.ID, u.Name, nullIfEmpty(u.PasswordHash), u.TOTPSecretEnc)
	if err != nil {
		return fmt.Errorf("store: create user: %w", err)
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	var passwordHash sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, password_hash, totp_secret_enc FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &passwordHash, &u.TOTPSecretEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: get user %q: %w", id, err)
	}
	u.PasswordHash = passwordHash.String
	return u, nil
}

// AnyUserExists reports whether any user has been enrolled yet, used to
// gate first-run enrolment (CLAUDE.md: "No default credentials, ever").
func (s *Store) AnyUserExists(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, fmt.Errorf("store: count users: %w", err)
	}
	return n > 0, nil
}

func (s *Store) SetUserTOTPSecret(ctx context.Context, userID string, encSecret []byte) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret_enc = ? WHERE id = ?`, encSecret, userID)
	if err != nil {
		return fmt.Errorf("store: set totp secret for %q: %w", userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Credential is one registered WebAuthn passkey.
type Credential struct {
	ID        []byte
	UserID    string
	Data      []byte // go-webauthn's serialized webauthn.Credential
	CreatedAt time.Time
	LastUsed  *time.Time
}

func (s *Store) CreateCredential(ctx context.Context, c Credential) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO credentials (id, user_id, data, created_at, last_used) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.UserID, c.Data, c.CreatedAt.Unix(), unixPtr(c.LastUsed))
	if err != nil {
		return fmt.Errorf("store: create credential: %w", err)
	}
	return nil
}

func (s *Store) CredentialsByUser(ctx context.Context, userID string) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, data, created_at, last_used FROM credentials WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials for %q: %w", userID, err)
	}
	defer rows.Close()
	return scanCredentials(rows)
}

// AllCredentials returns every registered credential, used to resolve a
// discoverable ("usernameless") passkey login: the browser presents a
// credential ID and the server must find which user it belongs to.
func (s *Store) AllCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, data, created_at, last_used FROM credentials`)
	if err != nil {
		return nil, fmt.Errorf("store: list all credentials: %w", err)
	}
	defer rows.Close()
	return scanCredentials(rows)
}

func scanCredentials(rows *sql.Rows) ([]Credential, error) {
	var out []Credential
	for rows.Next() {
		var c Credential
		var createdAt int64
		var lastUsed sql.NullInt64
		if err := rows.Scan(&c.ID, &c.UserID, &c.Data, &createdAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("store: scan credential: %w", err)
		}
		c.CreatedAt = time.Unix(createdAt, 0).UTC()
		if lastUsed.Valid {
			t := time.Unix(lastUsed.Int64, 0).UTC()
			c.LastUsed = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCredentialLastUsed(ctx context.Context, id []byte, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE credentials SET last_used = ? WHERE id = ?`, at.Unix(), id); err != nil {
		return fmt.Errorf("store: update credential last_used: %w", err)
	}
	return nil
}

func (s *Store) DeleteCredential(ctx context.Context, id []byte, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Session is one logged-in session. TokenHash is the session token's
// SHA-256 hash — the raw token is never stored (CLAUDE.md: "stored
// hashed").
type Session struct {
	TokenHash   []byte
	UserID      string
	CreatedAt   time.Time
	LastSeen    time.Time
	StepUpUntil *time.Time
	IP          string
	UserAgent   string
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, created_at, last_seen, stepup_until, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.CreatedAt.Unix(), sess.LastSeen.Unix(),
		unixPtr(sess.StepUpUntil), nullIfEmpty(sess.IP), nullIfEmpty(sess.UserAgent))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, tokenHash []byte) (Session, error) {
	var sess Session
	var createdAt, lastSeen int64
	var stepUpUntil sql.NullInt64
	var ip, ua sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT token_hash, user_id, created_at, last_seen, stepup_until, ip, user_agent
		FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&sess.TokenHash, &sess.UserID, &createdAt, &lastSeen, &stepUpUntil, &ip, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: get session: %w", err)
	}
	sess.CreatedAt = time.Unix(createdAt, 0).UTC()
	sess.LastSeen = time.Unix(lastSeen, 0).UTC()
	if stepUpUntil.Valid {
		t := time.Unix(stepUpUntil.Int64, 0).UTC()
		sess.StepUpUntil = &t
	}
	sess.IP = ip.String
	sess.UserAgent = ua.String
	return sess, nil
}

func (s *Store) TouchSession(ctx context.Context, tokenHash []byte, lastSeen time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, lastSeen.Unix(), tokenHash); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

func (s *Store) SetSessionStepUp(ctx context.Context, tokenHash []byte, until time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET stepup_until = ? WHERE token_hash = ?`, until.Unix(), tokenHash); err != nil {
		return fmt.Errorf("store: set session step-up: %w", err)
	}
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// SessionsByUser lists a user's active sessions, newest first, for the
// Security screen's "active sessions" list.
func (s *Store) SessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT token_hash, user_id, created_at, last_seen, stepup_until, ip, user_agent
		FROM sessions WHERE user_id = ? ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for %q: %w", userID, err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		var createdAt, lastSeen int64
		var stepUpUntil sql.NullInt64
		var ip, ua sql.NullString
		if err := rows.Scan(&sess.TokenHash, &sess.UserID, &createdAt, &lastSeen, &stepUpUntil, &ip, &ua); err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		sess.CreatedAt = time.Unix(createdAt, 0).UTC()
		sess.LastSeen = time.Unix(lastSeen, 0).UTC()
		if stepUpUntil.Valid {
			t := time.Unix(stepUpUntil.Int64, 0).UTC()
			sess.StepUpUntil = &t
		}
		sess.IP = ip.String
		sess.UserAgent = ua.String
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteExpiredSessions removes sessions whose last_seen is older than
// idleCutoff or whose created_at is older than absoluteCutoff.
func (s *Store) DeleteExpiredSessions(ctx context.Context, idleCutoff, absoluteCutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen < ? OR created_at < ?`,
		idleCutoff.Unix(), absoluteCutoff.Unix())
	if err != nil {
		return fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return nil
}

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("store: not found")
