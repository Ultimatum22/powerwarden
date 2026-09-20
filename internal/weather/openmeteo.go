package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// OpenMeteo is a ForecastSource backed by the free, public Open-Meteo API
// (no API key required).
type OpenMeteo struct {
	Location Point
	// LookaheadHours is how many hours ahead to check for a thunderstorm
	// forecast. Zero uses a 6-hour default (CLAUDE.md: "in the next
	// hours").
	LookaheadHours int

	// BaseURL overrides the API host, for tests. Empty uses the real API.
	BaseURL string

	HTTPClient *http.Client
}

const openMeteoDefaultBaseURL = "https://api.open-meteo.com"

// thunderstormWeatherCodes are Open-Meteo/WMO codes 95 (thunderstorm),
// 96 and 99 (thunderstorm with slight/heavy hail) — CLAUDE.md's explicit
// trigger codes.
var thunderstormWeatherCodes = map[int]bool{95: true, 96: true, 99: true}

type openMeteoResponse struct {
	Hourly struct {
		Time        []string  `json:"time"`
		WeatherCode []int     `json:"weathercode"`
		CAPE        []float64 `json:"cape"`
	} `json:"hourly"`
}

func (o OpenMeteo) Forecast(ctx context.Context) (Forecast, error) {
	lookahead := o.LookaheadHours
	if lookahead <= 0 {
		lookahead = 6
	}
	base := o.BaseURL
	if base == "" {
		base = openMeteoDefaultBaseURL
	}

	q := url.Values{
		"latitude":       {strconv.FormatFloat(o.Location.Lat, 'f', -1, 64)},
		"longitude":      {strconv.FormatFloat(o.Location.Lon, 'f', -1, 64)},
		"hourly":         {"weathercode,cape"},
		"forecast_hours": {strconv.Itoa(lookahead)},
		"timezone":       {"UTC"},
	}
	reqURL := base + "/v1/forecast?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Forecast{}, fmt.Errorf("weather: build open-meteo request: %w", err)
	}

	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Forecast{}, fmt.Errorf("weather: open-meteo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return Forecast{}, fmt.Errorf("weather: open-meteo returned status %d: %s", resp.StatusCode, body)
	}

	var data openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Forecast{}, fmt.Errorf("weather: decode open-meteo response: %w", err)
	}

	var f Forecast
	for i, code := range data.Hourly.WeatherCode {
		if thunderstormWeatherCodes[code] {
			f.ThunderstormExpected = true
		}
		if i < len(data.Hourly.CAPE) && data.Hourly.CAPE[i] > f.CAPEJPerKG {
			f.CAPEJPerKG = data.Hourly.CAPE[i]
		}
	}
	return f, nil
}

var _ ForecastSource = OpenMeteo{}
