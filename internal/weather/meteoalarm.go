package weather

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MeteoAlarm is a WarningSource backed by MeteoAlarm's public per-country
// Atom/CAP feeds (https://feeds.meteoalarm.org/), no API key required.
type MeteoAlarm struct {
	// Country is the feed's country slug, e.g. "netherlands" — see
	// https://feeds.meteoalarm.org/ for the list.
	Country string

	// BaseURL overrides the feed host, for tests. Empty uses the real
	// feeds.meteoalarm.org.
	BaseURL string

	HTTPClient *http.Client
}

const meteoAlarmDefaultBaseURL = "https://feeds.meteoalarm.org"

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	Event   string `xml:"event"`
	Onset   string `xml:"onset"`
	Expires string `xml:"expires"`
}

// color extracts the leading color word MeteoAlarm always prefixes titles
// with, e.g. "Orange Thunderstorm Warning issued for..." -> "orange". The
// feed doesn't otherwise expose a plain awareness-level field on every
// entry, so this is the most reliable signal available without parsing
// the linked per-warning CAP XML for its geocode.
func (e atomEntry) color() string {
	fields := strings.Fields(e.Title)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func (m MeteoAlarm) ActiveWarnings(ctx context.Context) ([]AlertWarning, error) {
	base := m.BaseURL
	if base == "" {
		base = meteoAlarmDefaultBaseURL
	}
	reqURL := fmt.Sprintf("%s/feeds/meteoalarm-legacy-atom-%s", base, m.Country)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("weather: build meteoalarm request: %w", err)
	}

	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weather: meteoalarm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("weather: meteoalarm returned status %d: %s", resp.StatusCode, body)
	}

	var feed atomFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("weather: decode meteoalarm feed: %w", err)
	}

	out := make([]AlertWarning, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		w := AlertWarning{Event: e.Event, Color: e.color()}
		if t, err := time.Parse(time.RFC3339, e.Onset); err == nil {
			w.Onset = t
		}
		if t, err := time.Parse(time.RFC3339, e.Expires); err == nil {
			w.Expires = t
		}
		out = append(out, w)
	}
	return out, nil
}

var _ WarningSource = MeteoAlarm{}
