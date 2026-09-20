// Package auth implements labpower's authentication: WebAuthn passkeys
// (primary), TOTP (backup), hashed sessions with idle/absolute timeouts,
// step-up for sensitive actions, rate limiting, and first-run enrolment.
// See CLAUDE.md's "Security requirements" — these apply from milestone 1
// even while the UI is VPN/Pangolin-private.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

// CookieName is the session cookie, using the __Host- prefix so browsers
// enforce Secure, no Domain attribute, and Path=/ on our behalf.
const CookieName = "__Host-labpower"

// tokenBytes matches CLAUDE.md: "Sessions: 32 random bytes, stored hashed".
const tokenBytes = 32

// Sessions manages session creation, validation, and step-up.
type Sessions struct {
	Store           *store.Store
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	StepUpDuration  time.Duration
	Secure          bool // false only for local (non-HTTPS) development/tests
}

// NewSessionToken generates a new random session token (the raw value to
// put in the cookie — never stored).
func NewSessionToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the value stored in place of the raw token.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Create starts a new session for userID and returns the raw token to set
// as a cookie (CLAUDE.md: "New token on login").
func (s *Sessions) Create(ctx context.Context, userID, ip, userAgent string, now time.Time) (string, error) {
	raw, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	sess := store.Session{
		TokenHash: HashToken(raw),
		UserID:    userID,
		CreatedAt: now,
		LastSeen:  now,
		IP:        ip,
		UserAgent: userAgent,
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return "", err
	}
	return raw, nil
}

var (
	// ErrNoSession means no session cookie was presented.
	ErrNoSession = errors.New("auth: no session")
	// ErrSessionExpired means idle or absolute timeout was exceeded.
	ErrSessionExpired = errors.New("auth: session expired")
	// ErrStepUpRequired means the route needs a fresh passkey assertion.
	ErrStepUpRequired = errors.New("auth: step-up required")
)

// Validate looks up the session for raw, checking idle and absolute
// timeouts, and touches last_seen on success. An expired session is
// deleted so it can't be revived.
func (s *Sessions) Validate(ctx context.Context, raw string, now time.Time) (store.Session, error) {
	hash := HashToken(raw)
	sess, err := s.Store.GetSession(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, ErrNoSession
	}
	if err != nil {
		return store.Session{}, err
	}

	if now.Sub(sess.LastSeen) > s.IdleTimeout || now.Sub(sess.CreatedAt) > s.AbsoluteTimeout {
		_ = s.Store.DeleteSession(ctx, hash)
		return store.Session{}, ErrSessionExpired
	}

	if err := s.Store.TouchSession(ctx, hash, now); err != nil {
		return store.Session{}, err
	}
	sess.LastSeen = now
	return sess, nil
}

// StepUp marks a session as having a fresh passkey assertion, valid for
// StepUpDuration (CLAUDE.md: "valid 5 min").
func (s *Sessions) StepUp(ctx context.Context, tokenHash []byte, now time.Time) error {
	return s.Store.SetSessionStepUp(ctx, tokenHash, now.Add(s.StepUpDuration))
}

// HasStepUp reports whether sess currently satisfies step-up.
func HasStepUp(sess store.Session, now time.Time) bool {
	return sess.StepUpUntil != nil && sess.StepUpUntil.After(now)
}

// Revoke deletes a session by its raw token (logout, or the Security
// screen's "Revoke" action from a token hash the caller already has).
func (s *Sessions) Revoke(ctx context.Context, tokenHash []byte) error {
	return s.Store.DeleteSession(ctx, tokenHash)
}

// Cookie builds the __Host-labpower cookie for raw. maxAge<=0 deletes it.
func (s *Sessions) Cookie(raw string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

// TokenFromRequest extracts the raw session token from the request's
// cookie, if present.
func TokenFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

// constantTimeEqual is a small helper for comparing tokens/secrets without
// leaking timing information.
func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
