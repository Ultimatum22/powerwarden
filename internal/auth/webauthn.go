package auth

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// NewWebAuthn builds the relying-party configuration. origins are the
// full https:// origins the UI is served from (CLAUDE.md's public_url).
func NewWebAuthn(rpID, rpDisplayName string, origins []string) (*webauthn.WebAuthn, error) {
	w, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     origins,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: init webauthn: %w", err)
	}
	return w, nil
}

// WebAuthnUser adapts a store user + their credentials to go-webauthn's
// User interface.
type WebAuthnUser struct {
	ID          string
	Name        string
	Credentials []webauthn.Credential
}

func (u WebAuthnUser) WebAuthnID() []byte                         { return []byte(u.ID) }
func (u WebAuthnUser) WebAuthnName() string                       { return u.Name }
func (u WebAuthnUser) WebAuthnDisplayName() string                { return u.Name }
func (u WebAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// EncodeCredential/DecodeCredential (de)serialize a webauthn.Credential
// for the credentials table's BLOB column.
func EncodeCredential(c webauthn.Credential) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("auth: encode credential: %w", err)
	}
	return b, nil
}

func DecodeCredential(data []byte) (webauthn.Credential, error) {
	var c webauthn.Credential
	if err := json.Unmarshal(data, &c); err != nil {
		return webauthn.Credential{}, fmt.Errorf("auth: decode credential: %w", err)
	}
	return c, nil
}

// ChallengeStore holds in-flight WebAuthn ceremony state between a
// /begin and /finish call. It's in-memory and single-use: a ceremony not
// completed within its TTL (or completed once) simply has to be retried,
// which is an acceptable cost for a step that normally takes seconds.
type ChallengeStore struct {
	mu      sync.Mutex
	entries map[string]challengeEntry
}

type challengeEntry struct {
	session webauthn.SessionData
	expires time.Time
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{entries: make(map[string]challengeEntry)}
}

// Put stores session under a new random ID and returns it, for the caller
// to round-trip to the client (e.g. in the JSON response body) and back on
// the matching /finish call.
func (c *ChallengeStore) Put(id string, session webauthn.SessionData, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = challengeEntry{session: session, expires: now.Add(ttl)}
}

// Take retrieves and removes the session for id (one-time use), or
// ok=false if it's missing or expired.
func (c *ChallengeStore) Take(id string, now time.Time) (webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	delete(c.entries, id)
	if !ok || now.After(e.expires) {
		return webauthn.SessionData{}, false
	}
	return e.session, true
}

// Prune removes expired entries, so a long-running process doesn't
// accumulate abandoned ceremonies.
func (c *ChallengeStore) Prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}
