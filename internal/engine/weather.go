package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/store"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

const (
	weatherLevelKey         = "weather_level"
	weatherStaleNotifiedKey = "weather_stale_notified"
)

// reconcileWeather evaluates the current threat level and reports level
// changes as events and notifications. It ships notify-only: nothing here
// changes what the host or guest reconciliation above does with the
// level — that's the weather-enforce milestone's job. Failures here are
// logged, not returned, so a weather subsystem hiccup (e.g. a transient
// store error) never blocks the safety-critical host/guest logic that
// follows it in Tick.
func (e *Engine) reconcileWeather(ctx context.Context, now time.Time) {
	if e.Weather == nil {
		return
	}
	result := e.Weather.Evaluate(ctx, now)

	lastLevel, err := e.storedWeatherLevel(ctx)
	if err != nil {
		e.Logger.Error("engine: load weather level", "error", err)
		return
	}

	if result.Level != lastLevel {
		if err := e.Store.RecordEvent(ctx, store.Event{
			At: now, Kind: "weather_level", Actor: "weather",
			Reason: fmt.Sprintf("%s -> %s", lastLevel, result),
		}); err != nil {
			e.Logger.Error("engine: record weather_level event", "error", err)
		}
		e.Logger.Info("engine: weather level changed", "from", lastLevel, "to", result.Level, "detail", result.String())

		// CLAUDE.md: "Notify on: ... weather level >= Warning ...". A
		// transition down from Warning/Danger (the storm passing) is
		// exactly as actionable to the owner as one going up, so both
		// directions across that threshold notify.
		if result.Level >= weather.Warning || lastLevel >= weather.Warning {
			e.notifyBestEffort(ctx, notify.Notification{
				Title:    "labpower weather",
				Body:     fmt.Sprintf("Weather level: %s -> %s (%s)", lastLevel, result.Level, result),
				Priority: weatherPriority(result.Level),
			})
		}
		if err := e.Store.SetState(ctx, weatherLevelKey, result.Level.String()); err != nil {
			e.Logger.Error("engine: save weather level", "error", err)
		}
	}

	e.reconcileStaleNotification(ctx, result)
}

// reconcileStaleNotification implements CLAUDE.md's "if stale during
// Normal/Watch, keep running and notify" — edge-triggered so it notifies
// once per stale episode, not every tick.
func (e *Engine) reconcileStaleNotification(ctx context.Context, result weather.Result) {
	shouldNotify := result.Stale && result.Level < weather.Warning
	notifiedStr, _, err := e.Store.GetState(ctx, weatherStaleNotifiedKey)
	if err != nil {
		e.Logger.Error("engine: load weather stale-notified flag", "error", err)
		return
	}
	alreadyNotified := notifiedStr == "true"

	switch {
	case shouldNotify && !alreadyNotified:
		e.notifyBestEffort(ctx, notify.Notification{
			Title: "labpower weather",
			Body:  fmt.Sprintf("Weather data is stale (sources: %v); still running on the last known state", result.StaleSources),
		})
		if err := e.Store.SetState(ctx, weatherStaleNotifiedKey, "true"); err != nil {
			e.Logger.Error("engine: save weather stale-notified flag", "error", err)
		}
	case !shouldNotify && alreadyNotified:
		if err := e.Store.DeleteState(ctx, weatherStaleNotifiedKey); err != nil {
			e.Logger.Error("engine: clear weather stale-notified flag", "error", err)
		}
	}
}

func (e *Engine) storedWeatherLevel(ctx context.Context) (weather.Level, error) {
	s, ok, err := e.Store.GetState(ctx, weatherLevelKey)
	if err != nil {
		return weather.Normal, err
	}
	if !ok {
		return weather.Normal, nil
	}
	switch s {
	case "normal":
		return weather.Normal, nil
	case "watch":
		return weather.Watch, nil
	case "warning":
		return weather.Warning, nil
	case "danger":
		return weather.Danger, nil
	default:
		return weather.Normal, fmt.Errorf("engine: unrecognized stored weather level %q", s)
	}
}

func weatherPriority(l weather.Level) notify.Priority {
	if l >= weather.Danger {
		return notify.PriorityUrgent
	}
	if l >= weather.Warning {
		return notify.PriorityHigh
	}
	return notify.PriorityDefault
}
