package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func TestHealthzNoAuthAndLeaksNothing(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want \"ok\" exactly (must not leak details)", got)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "interest-cohort=()",
	}
	for header, want := range checks {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("expected a Content-Security-Policy header")
	}
}

func TestNoStoreOnAuthenticatedPages(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestDashboardRedirectsToLoginWithoutSession(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestPartialReturns401WithoutSessionNotRedirect(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/partials/host", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (fragments must not redirect)", rec.Code)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)

	h.Clock.Advance(31 * time.Minute) // past the 30-minute idle timeout
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect to login for an expired session", rec.Code)
	}
}

func TestValidSessionReachesDashboard(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

func TestCSRFRejectsMissingToken(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a missing CSRF token", rec.Code)
	}
}

func TestCSRFAcceptsCorrectToken(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	sess, err := h.Sessions.Validate(t.Context(), raw, h.Clock.Now())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	token := h.csrfToken(sess)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a correct CSRF token, body: %s", rec.Code, rec.Body.String())
	}
}

func TestCSRFRejectsTokenForDifferentSession(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)

	// A token derived from a different (nonexistent) session must not work.
	otherSess := store.Session{TokenHash: auth.HashToken("some-other-token")}
	wrongToken := h.csrfToken(otherSess)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	req.Header.Set("X-CSRF-Token", wrongToken)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a token bound to a different session", rec.Code)
	}
}

func TestStepUpRequiredForHostShutdown(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	sess, err := h.Sessions.Validate(t.Context(), raw, h.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	token := h.csrfToken(sess)

	req := httptest.NewRequest(http.MethodPost, "/host/shutdown", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without step-up", rec.Code)
	}
}

func TestStepUpAllowsHostShutdownOnceGranted(t *testing.T) {
	h := newTestServer(t)
	raw, hash := h.createTestSession(t)
	if err := h.Sessions.StepUp(t.Context(), hash, h.Clock.Now()); err != nil {
		t.Fatalf("StepUp: %v", err)
	}
	sess, err := h.Sessions.Validate(t.Context(), raw, h.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	token := h.csrfToken(sess)

	req := httptest.NewRequest(http.MethodPost, "/host/shutdown", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with step-up granted, body: %s", rec.Code, rec.Body.String())
	}
}

func TestCrossOriginRequestRejected(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected a cross-origin state-changing request to be rejected")
	}
}

func TestLoginRateLimitLocksOutAfterFailure(t *testing.T) {
	h := newTestServer(t)
	// First attempt: no user enrolled at all, so it genuinely fails.
	req := httptest.NewRequest(http.MethodPost, "/login/totp", stringsReader("code=000000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("first attempt: status = %d, want 401", rec.Code)
	}

	// Immediately after, the exponential lockout must already be active —
	// even a would-be-correct attempt is rejected before it's checked.
	req = httptest.NewRequest(http.MethodPost, "/login/totp", stringsReader("code=000000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status immediately after a failure = %d, want 429", rec.Code)
	}

	// Once the lockout window passes, attempts are allowed again (and
	// fail normally, since there's still no user).
	h.Clock.Advance(2 * time.Second)
	req = httptest.NewRequest(http.MethodPost, "/login/totp", stringsReader("code=000000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status after the lockout window passed = %d, want 401", rec.Code)
	}
}
