package auth

import (
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestNewWebAuthnBuildsConfig(t *testing.T) {
	w, err := NewWebAuthn("power.example.com", "labpower", []string{"https://power.example.com"})
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	if w.Config.RPID != "power.example.com" {
		t.Errorf("RPID = %q", w.Config.RPID)
	}
}

func TestEncodeDecodeCredentialRoundTrip(t *testing.T) {
	cred := webauthn.Credential{
		ID:        []byte{1, 2, 3, 4},
		PublicKey: []byte{5, 6, 7, 8},
	}
	data, err := EncodeCredential(cred)
	if err != nil {
		t.Fatalf("EncodeCredential: %v", err)
	}
	decoded, err := DecodeCredential(data)
	if err != nil {
		t.Fatalf("DecodeCredential: %v", err)
	}
	if string(decoded.ID) != string(cred.ID) || string(decoded.PublicKey) != string(cred.PublicKey) {
		t.Fatalf("decoded = %+v, want %+v", decoded, cred)
	}
}

func TestChallengeStorePutTake(t *testing.T) {
	cs := NewChallengeStore()
	now := time.Now()
	session := webauthn.SessionData{Challenge: "abc123"}
	cs.Put("id-1", session, now, time.Minute)

	got, ok := cs.Take("id-1", now.Add(30*time.Second))
	if !ok || got.Challenge != "abc123" {
		t.Fatalf("Take = (%+v, %v), want the stored session", got, ok)
	}

	// One-time use: a second Take must fail.
	if _, ok := cs.Take("id-1", now); ok {
		t.Fatal("expected the challenge to be consumed after the first Take")
	}
}

func TestChallengeStoreExpiry(t *testing.T) {
	cs := NewChallengeStore()
	now := time.Now()
	cs.Put("id-1", webauthn.SessionData{Challenge: "abc"}, now, time.Minute)

	if _, ok := cs.Take("id-1", now.Add(2*time.Minute)); ok {
		t.Fatal("expected an expired challenge to be rejected")
	}
}

func TestChallengeStoreUnknownID(t *testing.T) {
	cs := NewChallengeStore()
	if _, ok := cs.Take("nonexistent", time.Now()); ok {
		t.Fatal("expected Take on an unknown ID to fail")
	}
}

func TestChallengeStorePrune(t *testing.T) {
	cs := NewChallengeStore()
	now := time.Now()
	cs.Put("id-1", webauthn.SessionData{}, now, time.Minute)
	cs.Put("id-2", webauthn.SessionData{}, now, time.Hour)

	cs.Prune(now.Add(2 * time.Minute))

	cs.mu.Lock()
	_, gone := cs.entries["id-1"]
	_, kept := cs.entries["id-2"]
	cs.mu.Unlock()
	if gone {
		t.Error("expected the expired entry to be pruned")
	}
	if !kept {
		t.Error("expected the still-valid entry to remain")
	}
}
