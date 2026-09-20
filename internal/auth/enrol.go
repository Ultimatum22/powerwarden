package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

// EnrolTokenTTL matches CLAUDE.md: "valid 15 minutes".
const EnrolTokenTTL = 15 * time.Minute

const (
	enrolTokenHashKey = "enrol_token_hash"
	enrolExpiresAtKey = "enrol_expires_at"
)

// GenerateEnrolToken creates a new one-time enrolment token, invalidating
// any previous one, and returns the raw token for the CLI to print to the
// console. It refuses to run if a user already exists (CLAUDE.md: "No
// default credentials, ever" — enrolment only ever creates the first
// account).
func GenerateEnrolToken(ctx context.Context, st *store.Store, now time.Time) (string, error) {
	exists, err := st.AnyUserExists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return "", errors.New("auth: a user is already enrolled; enrolment is for first-run setup only")
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate enrol token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(b)

	if err := st.SetState(ctx, enrolTokenHashKey, base64.StdEncoding.EncodeToString(HashToken(raw))); err != nil {
		return "", err
	}
	if err := st.SetState(ctx, enrolExpiresAtKey, now.Add(EnrolTokenTTL).Format(time.RFC3339)); err != nil {
		return "", err
	}
	return raw, nil
}

// ValidateEnrolToken checks raw against the stored token, and that no user
// has been enrolled since it was issued. It does not consume the token —
// callers should call ConsumeEnrolToken once enrolment actually succeeds,
// so a failed attempt (e.g. the passkey ceremony itself failing) can be
// retried with the same token until it expires.
func ValidateEnrolToken(ctx context.Context, st *store.Store, raw string, now time.Time) error {
	exists, err := st.AnyUserExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("auth: enrolment already completed")
	}

	storedHash, ok, err := st.GetState(ctx, enrolTokenHashKey)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("auth: no enrolment in progress")
	}
	expiresStr, _, err := st.GetState(ctx, enrolExpiresAtKey)
	if err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return fmt.Errorf("auth: parse enrol token expiry: %w", err)
	}
	if now.After(expires) {
		return errors.New("auth: enrolment token expired")
	}
	storedHashBytes, err := base64.StdEncoding.DecodeString(storedHash)
	if err != nil {
		return fmt.Errorf("auth: decode stored enrol token hash: %w", err)
	}
	if !constantTimeEqual(storedHashBytes, HashToken(raw)) {
		return errors.New("auth: invalid enrolment token")
	}
	return nil
}

// ConsumeEnrolToken deletes the stored enrolment token, so it can't be
// reused after successful enrolment.
func ConsumeEnrolToken(ctx context.Context, st *store.Store) error {
	if err := st.DeleteState(ctx, enrolTokenHashKey); err != nil {
		return err
	}
	return st.DeleteState(ctx, enrolExpiresAtKey)
}
