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

// Every signed-in page and fragment renders (template errors surface as
// a 500 with a generic body), with overrides and events present so the
// optional branches run too.
func TestEveryPageRenders(t *testing.T) {
	h := newTestServer(t)
	raw, _ := h.createTestSession(t)
	ctx := t.Context()
	until := h.Clock.Now().Add(3 * time.Hour)
	for _, ov := range []store.Override{
		{Target: "vm-media", Action: "on", Until: &until},
		{Target: "host", Action: "off", Until: &until},
		{Target: "host", Action: "ignore_weather", Until: &until},
	} {
		ov.CreatedBy, ov.CreatedAt = "user:owner", h.Clock.Now()
		if _, err := h.Store.CreateOverride(ctx, ov); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 60 { // more than one events page
		if err := h.Store.RecordEvent(ctx, store.Event{At: h.Clock.Now().Add(time.Duration(i) * time.Second), Kind: "guest_start", Target: "vm-media", Actor: "schedule", DryRun: i%2 == 0}); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{
		"/", "/timeline", "/timeline?day=tomorrow", "/timeline?day=week", "/vacation", "/weather",
		"/events", "/events?kind=guest_start", "/security", "/host/shutdown",
		"/partials/host", "/partials/weather", "/partials/guests",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "ZgotmplZ") {
			t.Errorf("GET %s: html/template rejected an unsafe value (ZgotmplZ)", path)
		}
	}

	// The events page links to the next page, which renders too.
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "/events?before=") {
		t.Fatal("no link to older events with more than one page")
	}
}
