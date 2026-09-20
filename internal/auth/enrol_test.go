package auth

import (
	"context"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

func TestEnrolTokenGenerateAndValidate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	raw, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatalf("GenerateEnrolToken: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a non-empty token")
	}
	if err := ValidateEnrolToken(ctx, st, raw, now.Add(time.Minute)); err != nil {
		t.Fatalf("ValidateEnrolToken: %v", err)
	}
}

func TestEnrolTokenRejectsWrongValue(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := GenerateEnrolToken(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnrolToken(ctx, st, "wrong-token", now); err == nil {
		t.Fatal("expected an error for the wrong token value")
	}
}

func TestEnrolTokenExpires(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	raw, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnrolToken(ctx, st, raw, now.Add(EnrolTokenTTL+time.Minute)); err == nil {
		t.Fatal("expected the token to be expired after 15 minutes")
	}
}

func TestEnrolTokenRefusedWhenUserAlreadyExists(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateUser(ctx, store.User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateEnrolToken(ctx, st, now); err == nil {
		t.Fatal("expected GenerateEnrolToken to refuse once a user exists")
	}
}

func TestEnrolTokenValidateRefusedAfterUserCreated(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	raw, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, store.User{ID: "dave", Name: "Dave"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnrolToken(ctx, st, raw, now); err == nil {
		t.Fatal("expected validation to fail once enrolment already completed")
	}
}

func TestConsumeEnrolTokenPreventsReuse(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	raw, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConsumeEnrolToken(ctx, st); err != nil {
		t.Fatalf("ConsumeEnrolToken: %v", err)
	}
	if err := ValidateEnrolToken(ctx, st, raw, now); err == nil {
		t.Fatal("expected validation to fail after the token was consumed")
	}
}

func TestGenerateEnrolTokenInvalidatesPrevious(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	first, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateEnrolToken(ctx, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnrolToken(ctx, st, first, now); err == nil {
		t.Fatal("expected the first token to be invalidated by generating a second")
	}
	if err := ValidateEnrolToken(ctx, st, second, now); err != nil {
		t.Fatalf("expected the second (latest) token to validate: %v", err)
	}
}
