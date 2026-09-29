package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// softAuthenticator is a minimal software passkey (ES256, no
// attestation) so the real WebAuthn assertion path — challenge, origin,
// RP ID hash, signature — runs in tests instead of being bypassed.
type softAuthenticator struct {
	key       *ecdsa.PrivateKey
	credID    []byte
	rpID      string
	origin    string
	signCount uint32
}

func newSoftAuthenticator(t *testing.T, rpID, origin string) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softAuthenticator{key: key, credID: id, rpID: rpID, origin: origin}
}

// register stores the authenticator's credential for userID, as a
// completed enrolment would.
func (a *softAuthenticator) register(t *testing.T, h *testHarness, userID string) {
	t.Helper()
	data, err := auth.EncodeCredential(webauthn.Credential{
		ID:              a.credID,
		PublicKey:       a.coseKey(t),
		AttestationType: "none",
		Flags:           webauthn.CredentialFlags{UserPresent: true, UserVerified: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.CreateCredential(t.Context(), store.Credential{
		ID: a.credID, UserID: userID, Data: data, CreatedAt: h.Clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// coseKey is the credential public key in COSE form.
func (a *softAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	pub, err := a.key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw := pub.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: raw[1:33],
		YCoord: raw[33:],
	})
	if err != nil {
		t.Fatal(err)
	}
	return cose
}

// attest answers a registration begin response with a "none"
// attestation creating this authenticator's credential.
func (a *softAuthenticator) attest(t *testing.T, beginBody []byte) []byte {
	t.Helper()
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(beginBody, &begin); err != nil || begin.PublicKey.Challenge == "" {
		t.Fatalf("bad registration begin response %s: %v", beginBody, err)
	}
	clientData, _ := json.Marshal(map[string]any{
		"type": "webauthn.create", "challenge": begin.PublicKey.Challenge, "origin": a.origin, "crossOrigin": false,
	})

	rpHash := sha256.Sum256([]byte(a.rpID))
	authData := append(rpHash[:], 0x45) // user present + user verified + attested credential data
	authData = binary.BigEndian.AppendUint32(authData, 0)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.credID)))
	authData = append(authData, a.credID...)
	authData = append(authData, a.coseKey(t)...)

	attObj, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	body, _ := json.Marshal(map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(clientData), "attestationObject": b64(attObj)},
	})
	return body
}

// assert answers a /…/begin response with a signed assertion body for
// the matching /…/finish request.
func (a *softAuthenticator) assert(t *testing.T, beginBody []byte, userID string) []byte {
	t.Helper()
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(beginBody, &begin); err != nil || begin.PublicKey.Challenge == "" {
		t.Fatalf("bad begin response %s: %v", beginBody, err)
	}

	clientData, _ := json.Marshal(map[string]any{
		"type": "webauthn.get", "challenge": begin.PublicKey.Challenge, "origin": a.origin, "crossOrigin": false,
	})
	rpHash := sha256.Sum256([]byte(a.rpID))
	a.signCount++
	authData := append(rpHash[:], 0x05) // flags: user present + user verified
	authData = binary.BigEndian.AppendUint32(authData, a.signCount)

	cdHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(bytes.Clone(authData), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	b64 := base64.RawURLEncoding.EncodeToString
	body, _ := json.Marshal(map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"authenticatorData": b64(authData),
			"signature":         b64(sig),
			"userHandle":        b64([]byte(userID)),
		},
	})
	return body
}

// stepUpClient drives signed-in requests for one session.
type stepUpClient struct {
	t    *testing.T
	h    *testHarness
	raw  string
	csrf string
}

func newStepUpClient(t *testing.T, h *testHarness) *stepUpClient {
	raw, _ := h.createTestSession(t)
	sess, err := h.Sessions.Validate(t.Context(), raw, h.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &stepUpClient{t: t, h: h, raw: raw, csrf: h.csrfToken(sess)}
}

func (c *stepUpClient) post(path, contentType string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.raw})
	req.Header.Set("X-CSRF-Token", c.csrf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.Handler().ServeHTTP(rec, req)
	return rec
}

func (c *stepUpClient) form(path string, v url.Values) int {
	return c.post(path, "application/x-www-form-urlencoded", []byte(v.Encode()), nil).Code
}

// stepUp runs /stepup/begin and /stepup/finish with a, returning the
// finish status code.
func (c *stepUpClient) stepUp(a *softAuthenticator) int {
	c.t.Helper()
	begin := c.post("/stepup/begin", "", nil, nil)
	if begin.Code != http.StatusOK {
		c.t.Fatalf("stepup/begin = %d: %s", begin.Code, begin.Body)
	}
	var ids struct {
		ChallengeID string `json:"challengeId"`
	}
	_ = json.Unmarshal(begin.Body.Bytes(), &ids)
	body := a.assert(c.t, begin.Body.Bytes(), store.SoleUserID)
	return c.post("/stepup/finish", "application/json", body, map[string]string{"X-Challenge-Id": ids.ChallengeID}).Code
}

// stepUpRequests hit every requireStepUp route in routes.go. Keep this
// list in sync when adding one: it's what proves the route is protected.
var stepUpRequests = []struct {
	path string
	form url.Values
}{
	{"/vacation", url.Values{"return": {"2026-01-20T09:00"}}},
	{"/vacation/end", url.Values{}},
	{"/weather/ignore", url.Values{"minutes": {"60"}}},
	{"/host/shutdown", url.Values{"wake": {"schedule"}}},
	{"/security/passkeys/begin", url.Values{}},
	{"/security/passkeys/finish", url.Values{}},
	{"/security/passkeys/00ff/delete", url.Values{}},
	{"/security/totp/setup", url.Values{}},
	{"/security/totp/confirm", url.Values{"secret": {"JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"}, "code": {"000000"}}},
	{"/security/totp/disable", url.Values{}},
}

func TestStepUpCeremonyUnlocksEveryProtectedRoute(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)

	for _, r := range stepUpRequests {
		if code := c.form(r.path, r.form); code != http.StatusForbidden {
			t.Fatalf("%s without step-up = %d, want 403", r.path, code)
		}
	}

	if code := c.stepUp(a); code != http.StatusOK {
		t.Fatalf("stepup/finish = %d, want 200", code)
	}
	for _, r := range stepUpRequests {
		// Past the step-up gate; some then fail validation (no pending
		// registration, unknown passkey), which is fine here.
		if code := c.form(r.path, r.form); code == http.StatusForbidden || code >= 500 {
			t.Fatalf("%s after step-up = %d, want the handler's own answer", r.path, code)
		}
	}

	// Step-up lasts 5 minutes (CLAUDE.md), then protected routes lock again.
	h.Clock.Advance(5*time.Minute + time.Second)
	c.csrf = c.refreshCSRF()
	if code := c.form("/weather/ignore", url.Values{"minutes": {"60"}}); code != http.StatusForbidden {
		t.Fatalf("/weather/ignore after step-up expired = %d, want 403", code)
	}
}

func (c *stepUpClient) refreshCSRF() string {
	sess, err := c.h.Sessions.Validate(c.t.Context(), c.raw, c.h.Clock.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	return c.h.csrfToken(sess)
}

func TestStepUpRejectsForeignPasskey(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	registered := newSoftAuthenticator(t, "localhost", "https://localhost")
	registered.register(t, h, store.SoleUserID)

	// Same credential ID, different private key: the signature can't verify.
	impostor := newSoftAuthenticator(t, "localhost", "https://localhost")
	impostor.credID = registered.credID
	if code := c.stepUp(impostor); code != http.StatusUnauthorized {
		t.Fatalf("stepup/finish with wrong key = %d, want 401", code)
	}
	if code := c.form("/host/shutdown", url.Values{"wake": {"schedule"}}); code != http.StatusForbidden {
		t.Fatalf("/host/shutdown after failed step-up = %d, want 403", code)
	}
}

func TestStepUpRejectsWrongOrigin(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	a.origin = "https://evil.example"
	if code := c.stepUp(a); code != http.StatusUnauthorized {
		t.Fatalf("stepup/finish from another origin = %d, want 401", code)
	}
}

func TestStepUpChallengeIsSingleUse(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)

	begin := c.post("/stepup/begin", "", nil, nil)
	var ids struct {
		ChallengeID string `json:"challengeId"`
	}
	_ = json.Unmarshal(begin.Body.Bytes(), &ids)
	body := a.assert(t, begin.Body.Bytes(), store.SoleUserID)
	hdr := map[string]string{"X-Challenge-Id": ids.ChallengeID}
	if code := c.post("/stepup/finish", "application/json", body, hdr).Code; code != http.StatusOK {
		t.Fatalf("first finish = %d, want 200", code)
	}
	if rec := c.post("/stepup/finish", "application/json", body, hdr); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("replayed finish = %d %q, want 400 expired", rec.Code, rec.Body)
	}
}
