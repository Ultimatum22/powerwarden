package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

func sentTitles(h *testHarness) []string {
	var out []string
	for _, n := range h.Notifier.(*notify.Fake).Sent() {
		out = append(out, n.Title)
	}
	return out
}

// passkeyLogin runs the discoverable login ceremony from ip and returns
// the finish status.
func passkeyLogin(t *testing.T, h *testHarness, a *softAuthenticator, ip string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login/passkey/begin", nil)
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login begin = %d", rec.Code)
	}
	var ids struct {
		ChallengeID string `json:"challengeId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ids)

	body := a.assert(t, rec.Body.Bytes(), store.SoleUserID)
	req = httptest.NewRequest(http.MethodPost, "/login/passkey/finish", strings.NewReader(string(body)))
	req.RemoteAddr = ip + ":40000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Challenge-Id", ids.ChallengeID)
	rec = httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func TestPasskeyLoginAlertsOnNewIPOnly(t *testing.T) {
	h := newTestServer(t)
	if err := h.Store.CreateUser(t.Context(), store.User{ID: store.SoleUserID, Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)

	for i, ip := range []string{"203.0.113.7", "203.0.113.7", "198.51.100.9"} {
		if code := passkeyLogin(t, h, a, ip); code != http.StatusOK {
			t.Fatalf("login %d from %s = %d, want 200", i+1, ip, code)
		}
	}
	titles := sentTitles(h)
	if len(titles) != 2 {
		t.Fatalf("notifications = %q, want one per new IP (2)", titles)
	}
	for _, title := range titles {
		if !strings.Contains(title, "new IP") {
			t.Fatalf("unexpected notification %q", title)
		}
	}
}

func TestFailedLoginBurstAlertsOncePerWindow(t *testing.T) {
	h := newTestServer(t)
	fail := func(i int) {
		req := httptest.NewRequest(http.MethodPost, "/login/totp", strings.NewReader("code=000000"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:40000", i) // spread over IPs, past the per-IP limiter
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, rec.Code)
		}
	}

	for i := 1; i < failBurstThreshold; i++ {
		fail(i)
	}
	if n := len(sentTitles(h)); n != 0 {
		t.Fatalf("alerted after %d failures, below the threshold", failBurstThreshold-1)
	}
	fail(failBurstThreshold)
	fail(failBurstThreshold + 1)
	titles := sentTitles(h)
	if len(titles) != 1 || !strings.Contains(titles[0], "failed sign-ins") {
		t.Fatalf("notifications = %q, want exactly one burst alert", titles)
	}

	// A new window alerts again.
	h.Clock.Advance(failBurstWindow + 1)
	for i := 10; i < 10+failBurstThreshold; i++ {
		fail(i)
	}
	if n := len(sentTitles(h)); n != 2 {
		t.Fatalf("notifications after a second burst = %d, want 2", n)
	}
}

func TestWeatherIgnoreNotifiesAndIsBounded(t *testing.T) {
	h := newTestServer(t)
	c := newStepUpClient(t, h)
	a := newSoftAuthenticator(t, "localhost", "https://localhost")
	a.register(t, h, store.SoleUserID)
	if code := c.stepUp(a); code != http.StatusOK {
		t.Fatalf("stepup = %d", code)
	}

	if code := c.form("/weather/ignore", url.Values{"minutes": {"100000"}}); code != http.StatusBadRequest {
		t.Fatalf("ignore weather for 100000 min = %d, want 400", code)
	}
	if code := c.form("/weather/ignore", url.Values{"minutes": {"30"}}); code != http.StatusOK {
		t.Fatalf("ignore weather for 30 min = %d, want 200", code)
	}
	titles := sentTitles(h)
	if len(titles) != 1 || !strings.Contains(titles[0], "weather safety bypassed") {
		t.Fatalf("notifications = %q, want one weather-bypass alert", titles)
	}
}
