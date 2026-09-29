package web

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// registrationRequiresResidentKey checks a registration begin response
// asks for a discoverable credential, which usernameless login needs.
func registrationRequiresResidentKey(t *testing.T, body []byte) {
	t.Helper()
	var begin struct {
		PublicKey struct {
			AuthenticatorSelection struct {
				ResidentKey string `json:"residentKey"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(body, &begin); err != nil {
		t.Fatal(err)
	}
	if got := begin.PublicKey.AuthenticatorSelection.ResidentKey; got != "required" {
		t.Fatalf("residentKey = %q, want required", got)
	}
}

func challengeID(t *testing.T, body []byte) string {
	t.Helper()
	var ids struct {
		ChallengeID string `json:"challengeId"`
	}
	if err := json.Unmarshal(body, &ids); err != nil || ids.ChallengeID == "" {
		t.Fatalf("no challengeId in %s", body)
	}
	return ids.ChallengeID
}

func TestEnrolmentRegistersDiscoverablePasskey(t *testing.T) {
	h := newTestServer(t)
	token, err := auth.GenerateEnrolToken(t.Context(), h.Store, h.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Enrol-Token", token)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, req)
		return rec
	}

	begin := post("/enrol/passkey/begin", nil, nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("enrol begin = %d", begin.Code)
	}
	registrationRequiresResidentKey(t, begin.Body.Bytes())

	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	finish := post("/enrol/passkey/finish", a.attest(t, begin.Body.Bytes()), map[string]string{"X-Challenge-Id": challengeID(t, begin.Body.Bytes())})
	if finish.Code != http.StatusOK {
		t.Fatalf("enrol finish = %d: %s", finish.Code, finish.Body)
	}
	// The enrolled passkey signs in.
	if code := passkeyLogin(t, h, a, "203.0.113.7"); code != http.StatusOK {
		t.Fatalf("login with the enrolled passkey = %d", code)
	}
}

func TestAddAndRemovePasskeys(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	first := newSoftAuthenticator(t, "localhost", "https://localhost")
	first.register(t, h, store.SoleUserID)
	if code := c.stepUp(first); code != http.StatusOK {
		t.Fatalf("stepup = %d", code)
	}

	// The only passkey can't be removed.
	if code := c.form("/security/passkeys/"+hex.EncodeToString(first.credID)+"/delete", nil); code != http.StatusConflict {
		t.Fatalf("removing the last passkey = %d, want 409", code)
	}

	begin := c.post("/security/passkeys/begin", "", nil, nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("add begin = %d", begin.Code)
	}
	registrationRequiresResidentKey(t, begin.Body.Bytes())
	second := newSoftAuthenticator(t, "localhost", "https://localhost")
	finish := c.post("/security/passkeys/finish", "application/json", second.attest(t, begin.Body.Bytes()),
		map[string]string{"X-Challenge-Id": challengeID(t, begin.Body.Bytes())})
	if finish.Code != http.StatusOK {
		t.Fatalf("add finish = %d: %s", finish.Code, finish.Body)
	}
	if code := passkeyLogin(t, h, second, "203.0.113.7"); code != http.StatusOK {
		t.Fatalf("login with the added passkey = %d", code)
	}

	// With two, the first can go, and then no longer signs in.
	if code := c.form("/security/passkeys/"+hex.EncodeToString(first.credID)+"/delete", nil); code != http.StatusOK {
		t.Fatalf("removing a passkey = %d, want 200", code)
	}
	if code := passkeyLogin(t, h, first, "203.0.113.7"); code == http.StatusOK {
		t.Fatal("a removed passkey still signs in")
	}
	if titles := strings.Join(sentTitles(h), "|"); strings.Count(titles, "security settings changed") != 2 {
		t.Fatalf("notifications = %q, want one per add/remove", titles)
	}
}

var secretInFragment = regexp.MustCompile(`name="secret" value="([A-Z2-7]+)"`)

func TestTOTPSetupConfirmAndLogin(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	if code := c.stepUp(a); code != http.StatusOK {
		t.Fatalf("stepup = %d", code)
	}

	setup := c.post("/security/totp/setup", "", nil, nil)
	if setup.Code != http.StatusOK || !strings.Contains(setup.Body.String(), `src="data:image/png;base64,`) {
		t.Fatalf("setup = %d, want a fragment with a QR code: %s", setup.Code, setup.Body)
	}
	m := secretInFragment.FindStringSubmatch(setup.Body.String())
	if m == nil {
		t.Fatalf("no secret in setup fragment: %s", setup.Body)
	}
	secret := m[1]

	// A wrong code re-shows the form and stores nothing.
	wrong := c.post("/security/totp/confirm", "application/x-www-form-urlencoded",
		[]byte(url.Values{"secret": {secret}, "code": {"000000"}}.Encode()), nil)
	if !strings.Contains(wrong.Body.String(), "match. Check the time") {
		t.Fatalf("wrong code response: %d %s", wrong.Code, wrong.Body)
	}
	if u, _ := h.Store.GetUser(t.Context(), store.SoleUserID); len(u.TOTPSecretEnc) != 0 {
		t.Fatal("secret stored after a wrong code")
	}

	code, _ := totp.GenerateCode(secret, h.Clock.Now())
	if got := c.form("/security/totp/confirm", url.Values{"secret": {secret}, "code": {code}}); got != http.StatusOK {
		t.Fatalf("confirm = %d", got)
	}

	// The backup login now works.
	req := httptest.NewRequest(http.MethodPost, "/login/totp", strings.NewReader("code="+code))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("totp login = %d", rec.Code)
	}

	if got := c.form("/security/totp/disable", nil); got != http.StatusOK {
		t.Fatalf("disable = %d", got)
	}
	if u, _ := h.Store.GetUser(t.Context(), store.SoleUserID); len(u.TOTPSecretEnc) != 0 {
		t.Fatal("secret still stored after disabling")
	}
}

func TestTOTPConfirmRejectsMalformedSecret(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	c.stepUp(a)
	if code := c.form("/security/totp/confirm", url.Values{"secret": {"abc&issuer=evil"}, "code": {"123456"}}); code != http.StatusBadRequest {
		t.Fatalf("malformed secret = %d, want 400", code)
	}
}

func TestRevokeOtherSession(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	other, err := h.Sessions.Create(t.Context(), store.SoleUserID, "198.51.100.9", "other", h.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherHash := hex.EncodeToString(auth.HashToken(other))

	if code := c.form("/security/sessions/"+otherHash+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if _, err := h.Sessions.Validate(t.Context(), other, h.Clock.Now()); err == nil {
		t.Fatal("revoked session still valid")
	}
	if code := c.form("/security/sessions/"+otherHash+"/revoke", nil); code != http.StatusNotFound {
		t.Fatalf("revoking again = %d, want 404", code)
	}
	if _, err := h.Sessions.Validate(t.Context(), c.raw, h.Clock.Now()); err != nil {
		t.Fatal("revoking another session ended this one")
	}
}

func TestHostShutdownWakesAtNextScheduleStart(t *testing.T) {
	h := newTestServer(t) // Monday 12:00 UTC; daytime is mon-fri 07:00–19:00
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	c.stepUp(a)

	if code := c.form("/host/shutdown", url.Values{"wake": {"schedule"}}); code != http.StatusOK {
		t.Fatalf("shutdown = %d", code)
	}
	ov, err := h.Store.EffectiveOverrideOf(t.Context(), "host", h.Clock.Now(), store.PowerActions...)
	if err != nil || ov == nil || ov.Action != "off" {
		t.Fatalf("override = %+v, %v", ov, err)
	}
	want := time.Date(2026, 1, 6, 7, 0, 0, 0, time.UTC)
	if ov.Until == nil || !ov.Until.Equal(want) {
		t.Fatalf("off until %v, want the next schedule start %v", ov.Until, want)
	}

	if code := c.form("/host/shutdown", url.Values{"wake": {"date"}, "date": {"2025-01-01T09:00"}}); code != http.StatusBadRequest {
		t.Fatalf("wake date in the past = %d, want 400", code)
	}
}

func TestVacationBannerOnlyForVacations(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	c.stepUp(a)

	dashboard := func() string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.raw})
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	const banner = "Vacation mode is active"

	if code := c.form("/host/shutdown", url.Values{"wake": {"manual"}}); code != http.StatusOK {
		t.Fatalf("manual shutdown = %d", code)
	}
	if strings.Contains(dashboard(), banner) {
		t.Fatal("vacation banner shown after a plain manual shutdown")
	}
	if code := c.form("/vacation/end", nil); code != http.StatusBadRequest {
		t.Fatalf("ending a vacation that isn't one = %d, want 400", code)
	}

	if code := c.form("/vacation", url.Values{"return": {"2026-01-20T09:00"}}); code != http.StatusOK {
		t.Fatalf("vacation = %d", code)
	}
	if !strings.Contains(dashboard(), banner) {
		t.Fatal("no vacation banner during a vacation")
	}
	if code := c.form("/vacation", url.Values{"return": {"2025-12-01T09:00"}}); code != http.StatusBadRequest {
		t.Fatalf("vacation ending in the past = %d, want 400", code)
	}
	if code := c.form("/vacation/end", nil); code != http.StatusOK {
		t.Fatalf("end vacation = %d", code)
	}
	if strings.Contains(dashboard(), banner) {
		t.Fatal("vacation banner still shown after ending it")
	}
}
