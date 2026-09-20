// Package weather combines lightning, official warning, forecast, and
// local-sensor data into a single threat level. It ships in notify-only
// mode (see CLAUDE.md's "Weather safeguard"); nothing in this package
// shuts anything down — the engine decides what, if anything, to do with
// the level it reports.
package weather

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Level is the current storm threat, in ascending severity.
type Level int

const (
	Normal Level = iota
	Watch
	Warning
	Danger
)

func (l Level) String() string {
	switch l {
	case Normal:
		return "normal"
	case Watch:
		return "watch"
	case Warning:
		return "warning"
	case Danger:
		return "danger"
	default:
		return "unknown"
	}
}

// Strike is one lightning detection from the network source.
type Strike struct {
	At    time.Time
	Point Point
}

// LightningSource reports recent strikes within roughly box (a coarse
// pre-filter; the Monitor still haversine-checks each one against the
// configured radii).
type LightningSource interface {
	Strikes(ctx context.Context, box BoundingBox) ([]Strike, error)
}

// AlertWarning is one active official warning.
type AlertWarning struct {
	Event   string // e.g. "Thunderstorm"
	Color   string // green | yellow | orange | red
	Onset   time.Time
	Expires time.Time
}

// IsThunderstorm reports whether the warning's event text indicates a
// thunderstorm (a simple, case-insensitive substring check: the exact
// wording varies a little by issuing authority, e.g. "Thunderstorms" vs
// "Thunderstorm warning").
func (w AlertWarning) IsThunderstorm() bool {
	return strings.Contains(strings.ToLower(w.Event), "thunder")
}

// SeriousColor reports whether the warning is orange or red, per
// CLAUDE.md's "official orange/red thunderstorm warning" trigger.
func (w AlertWarning) SeriousColor() bool {
	c := strings.ToLower(w.Color)
	return c == "orange" || c == "red"
}

// WarningSource reports currently active official warnings for the
// configured region.
type WarningSource interface {
	ActiveWarnings(ctx context.Context) ([]AlertWarning, error)
}

// Forecast summarizes near-term thunderstorm risk.
type Forecast struct {
	// ThunderstormExpected is true if a thunderstorm is forecast in the
	// next few hours (Open-Meteo weather codes 95/96/99).
	ThunderstormExpected bool
	// CAPEJPerKG is the forecast's Convective Available Potential Energy,
	// for display/future use; no fixed numeric threshold is specified by
	// CLAUDE.md, so it isn't used to gate the level on its own.
	CAPEJPerKG float64
}

// ForecastSource reports the near-term forecast for the configured
// location.
type ForecastSource interface {
	Forecast(ctx context.Context) (Forecast, error)
}

// LocalDetection is one AS3935 lightning-interrupt event.
type LocalDetection struct {
	At time.Time
}

// LocalSensor reports the local AS3935 sensor's recent detections.
type LocalSensor interface {
	// Detections returns detections at or after since.
	Detections(ctx context.Context, since time.Time) ([]LocalDetection, error)
}

// Config configures a Monitor. Sources left nil are treated as disabled
// (as if config said enabled: false) and never consulted.
type Config struct {
	Location Point

	Lightning LightningSource
	Warnings  WarningSource
	Forecast  ForecastSource
	Local     LocalSensor

	WarningRadiusKM  float64
	WarningCountdown time.Duration
	DangerRadiusKM   float64
	AllClearAfter    time.Duration
	StaleAfter       time.Duration

	// LocalCorroborationWindow is how close together two local detections
	// must be to count as confirmed on their own (CLAUDE.md: "two local
	// detections occur within 5 minutes"). Zero uses a 5-minute default.
	LocalCorroborationWindow time.Duration
}

// Monitor evaluates the current threat level from whichever sources are
// configured. It's safe for concurrent use.
type Monitor struct {
	cfg Config

	mu     sync.Mutex
	lastOK map[string]time.Time // source name -> last successful fetch
}

// NewMonitor builds a Monitor. now seeds a grace baseline for every
// configured source, so one that has never yet succeeded isn't treated as
// stale until a full stale_after has elapsed since startup, rather than on
// the very first Evaluate call.
func NewMonitor(cfg Config, now time.Time) *Monitor {
	if cfg.LocalCorroborationWindow <= 0 {
		cfg.LocalCorroborationWindow = 5 * time.Minute
	}
	lastOK := make(map[string]time.Time)
	for name, configured := range map[string]bool{
		"lightning": cfg.Lightning != nil,
		"warnings":  cfg.Warnings != nil,
		"forecast":  cfg.Forecast != nil,
		"local":     cfg.Local != nil,
	} {
		if configured {
			lastOK[name] = now
		}
	}
	return &Monitor{cfg: cfg, lastOK: lastOK}
}

// Result is the outcome of one Evaluate call, with enough detail to build
// a useful notification or UI card.
type Result struct {
	Level Level

	NearestStrikeKM float64 // -1 if no strike source or no strikes in range
	ActiveWarning   *AlertWarning
	ForecastThunder bool
	LocalConfirmed  bool
	Stale           bool
	StaleSources    []string
}

func (r Result) String() string {
	switch {
	case r.LocalConfirmed:
		return fmt.Sprintf("%s (local sensor confirmed)", r.Level)
	case r.NearestStrikeKM >= 0:
		return fmt.Sprintf("%s (nearest strike %.1fkm)", r.Level, r.NearestStrikeKM)
	case r.ActiveWarning != nil:
		return fmt.Sprintf("%s (%s %s warning)", r.Level, r.ActiveWarning.Color, r.ActiveWarning.Event)
	default:
		return r.Level.String()
	}
}

// Evaluate computes the current threat level from every configured
// source. A source that errors is treated as unavailable for this call
// (not fatal — see the stale-data handling below) and logged by the
// caller if it cares to.
func (m *Monitor) Evaluate(ctx context.Context, now time.Time) Result {
	m.mu.Lock()
	defer m.mu.Unlock()

	var nearestStrikeKM = -1.0
	if m.cfg.Lightning != nil {
		radius := m.cfg.WarningRadiusKM
		if m.cfg.DangerRadiusKM > radius {
			radius = m.cfg.DangerRadiusKM
		}
		box := BoundingBoxAround(m.cfg.Location, radius)
		strikes, err := m.cfg.Lightning.Strikes(ctx, box)
		if err == nil {
			m.lastOK["lightning"] = now
			for _, s := range strikes {
				d := DistanceKM(m.cfg.Location, s.Point)
				if nearestStrikeKM < 0 || d < nearestStrikeKM {
					nearestStrikeKM = d
				}
			}
		}
	}

	var activeWarning *AlertWarning
	if m.cfg.Warnings != nil {
		warnings, err := m.cfg.Warnings.ActiveWarnings(ctx)
		if err == nil {
			m.lastOK["warnings"] = now
			for i := range warnings {
				w := warnings[i]
				if w.IsThunderstorm() && w.SeriousColor() && !w.Expires.Before(now) {
					activeWarning = &w
					break
				}
			}
		}
	}

	forecastThunder := false
	if m.cfg.Forecast != nil {
		f, err := m.cfg.Forecast.Forecast(ctx)
		if err == nil {
			m.lastOK["forecast"] = now
			forecastThunder = f.ThunderstormExpected
		}
	}

	localConfirmed := false
	if m.cfg.Local != nil {
		dets, err := m.cfg.Local.Detections(ctx, now.Add(-m.cfg.LocalCorroborationWindow))
		if err == nil {
			m.lastOK["local"] = now
			localConfirmed = m.confirmLocal(dets, now, nearestStrikeKM)
		}
	}

	level := Normal
	switch {
	case nearestStrikeKM >= 0 && nearestStrikeKM <= m.cfg.DangerRadiusKM:
		level = Danger
	case localConfirmed:
		level = Danger
	case activeWarning != nil:
		level = Warning
	case nearestStrikeKM >= 0 && nearestStrikeKM <= m.cfg.WarningRadiusKM:
		level = Warning
	case forecastThunder:
		level = Watch
	}

	stale, staleSources := m.staleness(now, level)
	if stale && level >= Warning {
		level = Danger
	}

	return Result{
		Level:           level,
		NearestStrikeKM: nearestStrikeKM,
		ActiveWarning:   activeWarning,
		ForecastThunder: forecastThunder,
		LocalConfirmed:  localConfirmed,
		Stale:           stale,
		StaleSources:    staleSources,
	}
}

// confirmLocal applies CLAUDE.md's AS3935 noise filter: a single detection
// only counts if another source agrees (a network strike already inside
// the danger radius) or if two local detections land within the
// corroboration window of each other.
func (m *Monitor) confirmLocal(dets []LocalDetection, now time.Time, nearestStrikeKM float64) bool {
	if len(dets) == 0 {
		return false
	}
	agreement := nearestStrikeKM >= 0 && nearestStrikeKM <= m.cfg.DangerRadiusKM
	if agreement {
		return true
	}
	if len(dets) < 2 {
		return false
	}
	for i := 0; i < len(dets); i++ {
		for j := i + 1; j < len(dets); j++ {
			if absDuration(dets[i].At.Sub(dets[j].At)) <= m.cfg.LocalCorroborationWindow {
				return true
			}
		}
	}
	return false
}

// staleness reports whether any source relevant to the current tentative
// level hasn't produced a successful fetch within stale_after, per
// CLAUDE.md: "if data is older than stale_after while the level is
// Warning or higher, treat as Danger. If stale during Normal/Watch, keep
// running and notify" (the second half is the caller's job — Evaluate
// always returns Stale so it can decide whether to notify).
func (m *Monitor) staleness(now time.Time, level Level) (bool, []string) {
	if m.cfg.StaleAfter <= 0 {
		return false, nil
	}
	var stale []string
	check := func(name string, configured bool) {
		if !configured {
			return
		}
		last, ok := m.lastOK[name]
		if !ok || now.Sub(last) > m.cfg.StaleAfter {
			stale = append(stale, name)
		}
	}
	check("lightning", m.cfg.Lightning != nil)
	check("warnings", m.cfg.Warnings != nil)
	check("forecast", m.cfg.Forecast != nil)
	check("local", m.cfg.Local != nil)
	return len(stale) > 0, stale
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
