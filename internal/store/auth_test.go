package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUserCreateGetAndExists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if exists, err := s.AnyUserExists(ctx); err != nil || exists {
		t.Fatalf("AnyUserExists before any created = (%v, %v), want (false, nil)", exists, err)
	}

	if err := s.CreateUser(ctx, User{ID: "dave", Name: "Dave", TOTPSecretEnc: []byte("enc-secret")}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if exists, err := s.AnyUserExists(ctx); err != nil || !exists {
		t.Fatalf("AnyUserExists after creation = (%v, %v), want (true, nil)", exists, err)
	}

	u, err := s.GetUser(ctx, "dave")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.Name != "Dave" || string(u.TOTPSecretEnc) != "enc-secret" {
		t.Fatalf("GetUser = %+v", u)
	}

	if _, err := s.GetUser(ctx, "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(nonexistent) = %v, want ErrNotFound", err)
	}
}

func TestSetUserTOTPSecret(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserTOTPSecret(ctx, "dave", []byte("new-secret")); err != nil {
		t.Fatalf("SetUserTOTPSecret: %v", err)
	}
	u, err := s.GetUser(ctx, "dave")
	if err != nil {
		t.Fatal(err)
	}
	if string(u.TOTPSecretEnc) != "new-secret" {
		t.Fatalf("TOTPSecretEnc = %q, want new-secret", u.TOTPSecretEnc)
	}
	if err := s.SetUserTOTPSecret(ctx, "nonexistent", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetUserTOTPSecret(nonexistent) = %v, want ErrNotFound", err)
	}
}

func TestCredentialCreateListAndDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cred := Credential{ID: []byte{1, 2, 3}, UserID: "dave", Data: []byte("webauthn-blob"), CreatedAt: now}
	if err := s.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	creds, err := s.CredentialsByUser(ctx, "dave")
	if err != nil {
		t.Fatalf("CredentialsByUser: %v", err)
	}
	if len(creds) != 1 || string(creds[0].Data) != "webauthn-blob" {
		t.Fatalf("CredentialsByUser = %+v", creds)
	}
	if creds[0].LastUsed != nil {
		t.Fatalf("expected LastUsed nil before first use, got %v", creds[0].LastUsed)
	}

	all, err := s.AllCredentials(ctx)
	if err != nil {
		t.Fatalf("AllCredentials: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("AllCredentials = %+v, want 1", all)
	}

	usedAt := now.Add(time.Hour)
	if err := s.UpdateCredentialLastUsed(ctx, cred.ID, usedAt); err != nil {
		t.Fatalf("UpdateCredentialLastUsed: %v", err)
	}
	creds, _ = s.CredentialsByUser(ctx, "dave")
	if creds[0].LastUsed == nil || !creds[0].LastUsed.Equal(usedAt) {
		t.Fatalf("LastUsed = %v, want %v", creds[0].LastUsed, usedAt)
	}

	if err := s.DeleteCredential(ctx, cred.ID, "dave"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	creds, _ = s.CredentialsByUser(ctx, "dave")
	if len(creds) != 0 {
		t.Fatalf("expected no credentials after delete, got %+v", creds)
	}
	if err := s.DeleteCredential(ctx, cred.ID, "dave"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteCredential (already gone) = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tokenHash := []byte("hashed-token")
	sess := Session{TokenHash: tokenHash, UserID: "dave", CreatedAt: now, LastSeen: now, IP: "10.0.0.1", UserAgent: "test-agent"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.GetSession(ctx, tokenHash)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserID != "dave" || got.IP != "10.0.0.1" || got.StepUpUntil != nil {
		t.Fatalf("GetSession = %+v", got)
	}

	later := now.Add(10 * time.Minute)
	if err := s.TouchSession(ctx, tokenHash, later); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	got, _ = s.GetSession(ctx, tokenHash)
	if !got.LastSeen.Equal(later) {
		t.Fatalf("LastSeen = %v, want %v", got.LastSeen, later)
	}

	stepUpUntil := later.Add(5 * time.Minute)
	if err := s.SetSessionStepUp(ctx, tokenHash, stepUpUntil); err != nil {
		t.Fatalf("SetSessionStepUp: %v", err)
	}
	got, _ = s.GetSession(ctx, tokenHash)
	if got.StepUpUntil == nil || !got.StepUpUntil.Equal(stepUpUntil) {
		t.Fatalf("StepUpUntil = %v, want %v", got.StepUpUntil, stepUpUntil)
	}

	sessions, err := s.SessionsByUser(ctx, "dave")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("SessionsByUser = %+v, err=%v", sessions, err)
	}

	if err := s.DeleteSession(ctx, tokenHash); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession after delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fresh := Session{TokenHash: []byte("fresh"), UserID: "dave", CreatedAt: now, LastSeen: now}
	idleExpired := Session{TokenHash: []byte("idle"), UserID: "dave", CreatedAt: now, LastSeen: now.Add(-time.Hour)}
	absoluteExpired := Session{TokenHash: []byte("absolute"), UserID: "dave", CreatedAt: now.Add(-24 * time.Hour), LastSeen: now}
	for _, sess := range []Session{fresh, idleExpired, absoluteExpired} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}

	idleCutoff := now.Add(-30 * time.Minute)
	absoluteCutoff := now.Add(-12 * time.Hour)
	if err := s.DeleteExpiredSessions(ctx, idleCutoff, absoluteCutoff); err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}

	remaining, err := s.SessionsByUser(ctx, "dave")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || string(remaining[0].TokenHash) != "fresh" {
		t.Fatalf("remaining sessions = %+v, want only \"fresh\"", remaining)
	}
}
