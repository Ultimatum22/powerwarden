package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sampleFeed mirrors the real structure fetched from
// feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-netherlands.
const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:cap="urn:oasis:names:tc:emergency:cap:1.2">
  <id>tag:meteoalarm.org,2021-02-19:NL</id>
  <title>MeteoAlarm - Alerting Europe for Extreme Weather</title>
  <entry>
    <cap:areaDesc>Delfzijl</cap:areaDesc>
    <cap:event>Moderate Wind</cap:event>
    <cap:onset>2026-09-18T09:56:13+00:00</cap:onset>
    <cap:expires>2026-09-21T09:34:39+00:00</cap:expires>
    <cap:severity>Moderate</cap:severity>
    <title>Yellow Wind Warning issued for The Netherlands - Delfzijl</title>
  </entry>
  <entry>
    <cap:areaDesc>Amsterdam</cap:areaDesc>
    <cap:event>Thunderstorms</cap:event>
    <cap:onset>2026-09-20T09:00:00+00:00</cap:onset>
    <cap:expires>2026-09-20T18:00:00+00:00</cap:expires>
    <cap:severity>Severe</cap:severity>
    <title>Orange Thunderstorm Warning issued for The Netherlands - Amsterdam</title>
  </entry>
</feed>`

func TestMeteoAlarmParsesEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feeds/meteoalarm-legacy-atom-netherlands" {
			t.Errorf("path = %q, want the netherlands feed", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(sampleFeed))
	}))
	defer srv.Close()

	m := MeteoAlarm{Country: "netherlands", BaseURL: srv.URL}
	warnings, err := m.ActiveWarnings(context.Background())
	if err != nil {
		t.Fatalf("ActiveWarnings: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2", len(warnings))
	}

	wind, storm := warnings[0], warnings[1]
	if wind.Color != "yellow" || wind.IsThunderstorm() {
		t.Errorf("wind warning = %+v, want color=yellow, not a thunderstorm", wind)
	}
	if storm.Color != "orange" || !storm.IsThunderstorm() || !storm.SeriousColor() {
		t.Errorf("storm warning = %+v, want color=orange, IsThunderstorm, SeriousColor", storm)
	}
	if storm.Onset.IsZero() || storm.Expires.IsZero() {
		t.Errorf("storm warning onset/expires not parsed: %+v", storm)
	}
}

func TestMeteoAlarmErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	m := MeteoAlarm{Country: "nonexistent", BaseURL: srv.URL}
	if _, err := m.ActiveWarnings(context.Background()); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}
