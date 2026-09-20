package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenMeteoParsesThunderstormForecast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("latitude"); got != "52.37" {
			t.Errorf("latitude = %q, want 52.37", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"hourly": {
				"time": ["2026-09-20T11:00","2026-09-20T12:00","2026-09-20T13:00"],
				"weathercode": [3, 95, 1],
				"cape": [50.0, 800.0, 30.0]
			}
		}`))
	}))
	defer srv.Close()

	o := OpenMeteo{Location: Point{Lat: 52.37, Lon: 4.90}, BaseURL: srv.URL}
	f, err := o.Forecast(context.Background())
	if err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if !f.ThunderstormExpected {
		t.Error("expected ThunderstormExpected=true for weather code 95")
	}
	if f.CAPEJPerKG != 800.0 {
		t.Errorf("CAPEJPerKG = %v, want 800 (the max across the window)", f.CAPEJPerKG)
	}
}

func TestOpenMeteoNoThunderstormCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hourly": {"time": ["t"], "weathercode": [3], "cape": [10.0]}}`))
	}))
	defer srv.Close()

	o := OpenMeteo{Location: Point{Lat: 52.37, Lon: 4.90}, BaseURL: srv.URL}
	f, err := o.Forecast(context.Background())
	if err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if f.ThunderstormExpected {
		t.Error("expected ThunderstormExpected=false for a clear-sky code")
	}
}

func TestOpenMeteoErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	o := OpenMeteo{Location: Point{Lat: 52.37, Lon: 4.90}, BaseURL: srv.URL}
	if _, err := o.Forecast(context.Background()); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}
