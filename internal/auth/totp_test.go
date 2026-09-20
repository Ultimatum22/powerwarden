package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateAndValidateTOTP(t *testing.T) {
	secret, url, err := GenerateTOTPSecret("labpower", "dave")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if secret == "" || url == "" {
		t.Fatal("expected a non-empty secret and otpauth URL")
	}

	now := time.Now()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !ValidateTOTP(secret, code, now) {
		t.Fatal("expected the freshly generated code to validate")
	}
}

func TestValidateTOTPRejectsWrongCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("labpower", "dave")
	if err != nil {
		t.Fatal(err)
	}
	if ValidateTOTP(secret, "000000", time.Now()) {
		// Astronomically unlikely to be the real code, but guard against
		// flakiness by also checking a guaranteed-wrong second value.
		if ValidateTOTP(secret, "111111", time.Now()) {
			t.Fatal("expected an arbitrary 6-digit code to be rejected")
		}
	}
}

func TestSecretBoxRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	box, err := NewSecretBox(key)
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}

	sealed, err := box.Seal("my-totp-secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if string(sealed) == "my-totp-secret" {
		t.Fatal("Seal must not return plaintext")
	}

	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != "my-totp-secret" {
		t.Fatalf("Open = %q, want the original plaintext", opened)
	}
}

func TestSecretBoxRejectsTamperedCiphertext(t *testing.T) {
	key, _ := GenerateKey()
	box, _ := NewSecretBox(key)
	sealed, _ := box.Seal("secret")
	sealed[len(sealed)-1] ^= 0xFF // flip a bit near the end (inside the auth tag)

	if _, err := box.Open(sealed); err == nil {
		t.Fatal("expected tampered ciphertext to fail to decrypt")
	}
}

func TestSecretBoxRejectsWrongKey(t *testing.T) {
	key1, _ := GenerateKey()
	key2, _ := GenerateKey()
	box1, _ := NewSecretBox(key1)
	box2, _ := NewSecretBox(key2)

	sealed, _ := box1.Seal("secret")
	if _, err := box2.Open(sealed); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
	}
}

func TestNewSecretBoxRejectsBadKeyLength(t *testing.T) {
	if _, err := NewSecretBox([]byte("too-short")); err == nil {
		t.Fatal("expected an error for a non-32-byte key")
	}
}
