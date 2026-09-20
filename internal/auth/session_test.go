package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labpower.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testSessions(t *testing.T) *Sessions {
	return &Sessions{
		Store:           openTestStore(t),
		IdleTimeout:     30 * time.Minute,
		AbsoluteTimeout: 12 * time.Hour,
		StepUpDuration:  5 * time.Minute,
		Secure:          true,
	}
}

func TestSessionCreateAndValidate(t *testing.T) {
	s := testSessions(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	raw, err := s.Create(ctx, "dave", "10.0.0.1", "test-agent", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a non-empty raw token")
	}

	sess, err := s.Validate(ctx, raw, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if sess.UserID != "dave" {
		t.Fatalf("UserID = %q, want dave", sess.UserID)
	}
}

func TestSessionValidateRejectsUnknownToken(t *testing.T) {
	s := testSessions(t)
	if _, err := s.Validate(context.Background(), "not-a-real-token", time.Now()); err != ErrNoSession {
		t.Fatalf("Validate(unknown) = %v, want ErrNoSession", err)
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	s := testSessions(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	raw, err := s.Create(ctx, "dave", "", "", now)
	if err != nil {
		t.Fatal(err)
	}

	// Just under idle timeout: still valid.
	if _, err := s.Validate(ctx, raw, now.Add(29*time.Minute)); err != nil {
		t.Fatalf("Validate just under idle timeout: %v", err)
	}
	// Now last_seen was touched to now+29m; another 31 minutes of
	// inactivity should expire it.
	if _, err := s.Validate(ctx, raw, now.Add(29*time.Minute).Add(31*time.Minute)); err != ErrSessionExpired {
		t.Fatalf("Validate past idle timeout = %v, want ErrSessionExpired", err)
	}
	// And it must actually be gone, not just rejected this once.
	if _, err := s.Validate(ctx, raw, now); err != ErrNoSession {
		t.Fatalf("Validate after expiry-triggered deletion = %v, want ErrNoSession", err)
	}
}

func TestSessionAbsoluteTimeout(t *testing.T) {
	s := testSessions(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	raw, err := s.Create(ctx, "dave", "", "", now)
	if err != nil {
		t.Fatal(err)
	}

	// Touch it frequently (well within idle timeout each time) but past
	// the absolute 12h ceiling.
	if _, err := s.Validate(ctx, raw, now.Add(13*time.Hour)); err != ErrSessionExpired {
		t.Fatalf("Validate past absolute timeout = %v, want ErrSessionExpired", err)
	}
}

func TestSessionStepUp(t *testing.T) {
	s := testSessions(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	raw, err := s.Create(ctx, "dave", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Validate(ctx, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if HasStepUp(sess, now) {
		t.Fatal("expected no step-up immediately after creation")
	}

	if err := s.StepUp(ctx, sess.TokenHash, now); err != nil {
		t.Fatalf("StepUp: %v", err)
	}
	sess, err = s.Validate(ctx, raw, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !HasStepUp(sess, now.Add(time.Minute)) {
		t.Fatal("expected step-up to be active within the 5-minute window")
	}
	if HasStepUp(sess, now.Add(6*time.Minute)) {
		t.Fatal("expected step-up to have expired after 5 minutes")
	}
}

func TestSessionRevoke(t *testing.T) {
	s := testSessions(t)
	ctx := context.Background()
	now := time.Now()
	raw, err := s.Create(ctx, "dave", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Validate(ctx, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, sess.TokenHash); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Validate(ctx, raw, now); err != ErrNoSession {
		t.Fatalf("Validate after revoke = %v, want ErrNoSession", err)
	}
}

func TestCookieAttributes(t *testing.T) {
	s := testSessions(t)
	c := s.Cookie("raw-token", 3600)
	if c.Name != CookieName {
		t.Errorf("Name = %q, want %q", c.Name, CookieName)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie attributes = %+v, want HttpOnly+Secure+SameSiteStrict+Path=/", c)
	}
}

func TestHashTokenIsDeterministicAndDistinct(t *testing.T) {
	h1 := HashToken("token-a")
	h2 := HashToken("token-a")
	h3 := HashToken("token-b")
	if string(h1) != string(h2) {
		t.Error("HashToken should be deterministic")
	}
	if string(h1) == string(h3) {
		t.Error("HashToken should differ for different inputs")
	}
}
