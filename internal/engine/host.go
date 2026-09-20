package engine

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

const hostTarget = "host" // the overrides/events "target" naming the host itself

const (
	hostWakeAttemptsKey = "host_wake_attempts"
	hostWakeLastSentKey = "host_wake_last_sent_at"
	hostWakeFailedKey   = "host_wake_failed"

	hostShutdownPostponedNotifiedKey = "host_shutdown_postponed_notified"

	weatherWarningSinceKey    = "weather_warning_since"
	weatherDangerSinceKey     = "weather_danger_since"
	weatherDangerTaskGraceKey = "weather_danger_task_grace_since"
)

// reconcileHost decides whether the host needs to wake up or shut down
// this tick, and acts on it.
//
// Unlike guests, host reconciliation is level-triggered, not
// edge-triggered: it just compares what's desired against Proxmox's actual
// reachability every tick. That's safe here (repeating an already-applied
// decision is a cheap no-op, not a redundant API call) and it sidesteps
// the one reason guests need edge-triggering — "stays on until the next
// boundary" for a manual Proxmox-UI start — which has no equivalent for
// the host: nothing manages the host's power outside labpower and
// physical/WoL access.
//
// It returns notReady=true if the host is off, waking, or was just told
// to shut down this tick — in any of those cases Tick must not try to
// reconcile guests (there's no node to reach, or one that's about to
// disappear).
func (e *Engine) reconcileHost(ctx context.Context, now time.Time, reachable, anyGuestWantsOn bool, actual map[string]proxmox.Guest, weatherResult weather.Result) (notReady bool, err error) {
	forceOff, bypassActiveTasks, err := e.weatherSafety(ctx, now, weatherResult)
	if err != nil {
		return false, err
	}

	var desired, act bool
	if forceOff {
		// Safety beats manual override and schedule both (CLAUDE.md:
		// "Safety (weather...) > manual override > schedule"), so this
		// skips hostDesiredState (and therefore any "pause"/"on"
		// override) entirely.
		desired, act = false, true
	} else {
		hostSched := e.Schedules[e.Host.Schedule]
		baseDesired := hostSched.IsOn(now, e.Loc) || anyGuestWantsOn
		desired, act, err = e.hostDesiredState(ctx, now, baseDesired)
		if err != nil {
			return false, err
		}
	}
	if !act {
		return !reachable, nil // paused: leave the host exactly as it is
	}

	if !desired {
		if err := e.clearWakeState(ctx); err != nil {
			return false, err
		}
		if !reachable {
			return true, nil // already off
		}
		return e.shutdownHost(ctx, now, actual, bypassActiveTasks)
	}

	if reachable {
		if err := e.clearWakeState(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, e.pursueWake(ctx, now)
}

// weatherSafety implements CLAUDE.md's weather safeguard level table for
// the host: Warning shuts down after warning.countdown unless an
// ignore_weather override is active; Danger shuts down immediately,
// bypassing the usual indefinite wait for active tasks after
// host.shutdown_grace (not bypassing the check itself — shutdownHost
// still checks, it just won't wait past the grace period). Only takes
// effect when weather.mode is "enforce"; ships notify-only otherwise.
func (e *Engine) weatherSafety(ctx context.Context, now time.Time, result weather.Result) (forceOff, bypassActiveTasksAfterGrace bool, err error) {
	if e.WeatherMode != "enforce" || result.Level < weather.Warning {
		if err := e.clearWeatherEnforcementState(ctx); err != nil {
			return false, false, err
		}
		return false, false, nil
	}

	ov, err := e.Store.EffectiveOverride(ctx, hostTarget, now)
	if err != nil {
		return false, false, fmt.Errorf("engine: effective host override: %w", err)
	}
	if ov != nil && ov.Action == "ignore_weather" {
		return false, false, nil
	}

	if result.Level >= weather.Danger {
		return true, true, nil
	}

	// Warning: wait out the countdown, still respecting active tasks
	// normally (only Danger bypasses that).
	since, err := e.stateSince(ctx, weatherWarningSinceKey, now)
	if err != nil {
		return false, false, err
	}
	return now.Sub(since) >= e.WeatherWarningCountdown, false, nil
}

func (e *Engine) clearWeatherEnforcementState(ctx context.Context) error {
	for _, key := range []string{weatherWarningSinceKey, weatherDangerSinceKey, weatherDangerTaskGraceKey} {
		if err := e.Store.DeleteState(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// stateSince returns the timestamp stored at key, initializing it to now
// (and persisting that) the first time it's asked for — used to measure
// "how long has X been true" across ticks without a dedicated table.
func (e *Engine) stateSince(ctx context.Context, key string, now time.Time) (time.Time, error) {
	s, ok, err := e.Store.GetState(ctx, key)
	if err != nil {
		return time.Time{}, fmt.Errorf("engine: load %s: %w", key, err)
	}
	if ok {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("engine: parse %s %q: %w", key, s, err)
		}
		return t, nil
	}
	if err := e.Store.SetState(ctx, key, now.Format(time.RFC3339)); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// hostDesiredState applies "manual override beats schedule" to the host,
// the same priority rule guests follow.
func (e *Engine) hostDesiredState(ctx context.Context, now time.Time, baseDesired bool) (desired, act bool, err error) {
	ov, err := e.Store.EffectiveOverride(ctx, hostTarget, now)
	if err != nil {
		return false, false, fmt.Errorf("engine: effective host override: %w", err)
	}
	if ov != nil {
		switch ov.Action {
		case "on":
			return true, true, nil
		case "off":
			return false, true, nil
		case "pause":
			return false, false, nil
		}
	}
	return baseDesired, true, nil
}

// pursueWake sends (or resends) a WoL packet, tracking attempts across
// ticks in the state table so it survives a labpower restart mid-wake.
func (e *Engine) pursueWake(ctx context.Context, now time.Time) error {
	failedStr, _, err := e.Store.GetState(ctx, hostWakeFailedKey)
	if err != nil {
		return fmt.Errorf("engine: load %s: %w", hostWakeFailedKey, err)
	}
	if failedStr == "true" {
		return nil // already gave up this episode; a fresh one starts once desired next becomes false then true
	}

	lastSentStr, hasLastSent, err := e.Store.GetState(ctx, hostWakeLastSentKey)
	if err != nil {
		return fmt.Errorf("engine: load %s: %w", hostWakeLastSentKey, err)
	}

	if !hasLastSent {
		if err := e.sendWoL(ctx); err != nil {
			return fmt.Errorf("engine: send WoL: %w", err)
		}
		if err := e.Store.SetState(ctx, hostWakeAttemptsKey, "1"); err != nil {
			return err
		}
		if err := e.Store.SetState(ctx, hostWakeLastSentKey, now.Format(time.RFC3339)); err != nil {
			return err
		}
		e.Logger.Info("engine: sent Wake-on-LAN", "attempt", 1)
		e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Body: "Waking the Proxmox host"})
		return e.recordEvent(ctx, "host_wake", hostTarget, "schedule", e.DryRun)
	}

	lastSent, err := time.Parse(time.RFC3339, lastSentStr)
	if err != nil {
		return fmt.Errorf("engine: parse %s %q: %w", hostWakeLastSentKey, lastSentStr, err)
	}
	if now.Sub(lastSent) < e.WoLWakeTimeout {
		return nil // this attempt hasn't timed out yet
	}

	attempts, err := e.getIntState(ctx, hostWakeAttemptsKey)
	if err != nil {
		return err
	}
	if attempts >= e.WoLRetries {
		if err := e.Store.SetState(ctx, hostWakeFailedKey, "true"); err != nil {
			return err
		}
		e.Logger.Error("engine: host failed to wake", "attempts", attempts)
		e.notifyBestEffort(ctx, notify.Notification{
			Title: "labpower", Priority: notify.PriorityUrgent,
			Body: fmt.Sprintf("Host failed to wake after %d attempt(s)", attempts),
		})
		return e.recordEvent(ctx, "host_wake_failed", hostTarget, "schedule", e.DryRun)
	}

	if err := e.sendWoL(ctx); err != nil {
		return fmt.Errorf("engine: resend WoL: %w", err)
	}
	attempts++
	if err := e.Store.SetState(ctx, hostWakeAttemptsKey, strconv.Itoa(attempts)); err != nil {
		return err
	}
	if err := e.Store.SetState(ctx, hostWakeLastSentKey, now.Format(time.RFC3339)); err != nil {
		return err
	}
	e.Logger.Info("engine: resending Wake-on-LAN", "attempt", attempts)
	return nil
}

func (e *Engine) sendWoL(ctx context.Context) error {
	if e.DryRun {
		e.Logger.Info("dry-run: would send Wake-on-LAN", "mac", e.WoLMAC)
		return nil
	}
	return e.WoLSender.Send(ctx, e.WoLMAC)
}

func (e *Engine) clearWakeState(ctx context.Context) error {
	for _, key := range []string{hostWakeAttemptsKey, hostWakeLastSentKey, hostWakeFailedKey} {
		if err := e.Store.DeleteState(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// shutdownHost shuts down every currently-running, non-always-on guest
// (reverse dependency order — CLAUDE.md: "shuts down scheduled guests
// first"), then the node itself. It force-stops guests unconditionally,
// overriding the usual "manual start persists until the next boundary"
// rule: that rule exists to avoid needlessly interrupting guests, not to
// keep one running while the host underneath it disappears.
func (e *Engine) shutdownHost(ctx context.Context, now time.Time, actual map[string]proxmox.Guest, bypassActiveTasksAfterGrace bool) (notReady bool, err error) {
	tasks, err := e.Proxmox.ActiveTasks(ctx)
	if err != nil {
		return true, fmt.Errorf("engine: check active tasks: %w", err)
	}
	if len(tasks) > 0 {
		proceedAnyway := false
		if bypassActiveTasksAfterGrace {
			since, err := e.stateSince(ctx, weatherDangerTaskGraceKey, now)
			if err != nil {
				return true, err
			}
			proceedAnyway = now.Sub(since) >= e.Host.ShutdownGrace
		}
		if !proceedAnyway {
			notified, _, err := e.Store.GetState(ctx, hostShutdownPostponedNotifiedKey)
			if err != nil {
				return true, err
			}
			if notified != "true" {
				e.Logger.Warn("engine: host shutdown postponed, active tasks running", "tasks", len(tasks), "weather_grace", bypassActiveTasksAfterGrace)
				e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Body: "Host shutdown postponed: a task is still running"})
				if err := e.Store.SetState(ctx, hostShutdownPostponedNotifiedKey, "true"); err != nil {
					return true, err
				}
			}
			return true, nil
		}
		e.Logger.Error("engine: forcing host shutdown despite active tasks — weather Danger grace period elapsed", "tasks", len(tasks))
		e.notifyBestEffort(ctx, notify.Notification{
			Title: "labpower", Priority: notify.PriorityUrgent,
			Body: "Forcing host shutdown despite active tasks: weather Danger grace period elapsed",
		})
	}
	if err := e.Store.DeleteState(ctx, hostShutdownPostponedNotifiedKey); err != nil {
		return true, err
	}
	if err := e.Store.DeleteState(ctx, weatherDangerTaskGraceKey); err != nil {
		return true, err
	}

	for i := len(e.order) - 1; i >= 0; i-- {
		g, ok := guestByName(e.Guests, e.order[i])
		if !ok || g.AlwaysOn {
			continue
		}
		guest, found := actual[g.Name]
		if !found || guest.Status != proxmox.StatusRunning {
			continue
		}
		if err := e.execute(ctx, plannedAction{guest: g, vmid: guest.VMID, kind: guest.Kind, start: false, override: nil}); err != nil {
			return true, fmt.Errorf("engine: stop %q before host shutdown: %w", g.Name, err)
		}
	}

	if e.DryRun {
		e.Logger.Info("dry-run: would shut down host")
		e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Body: "Shutting down the Proxmox host"})
		return true, e.recordEvent(ctx, "host_shutdown", hostTarget, "schedule", true)
	}

	upid, err := e.Proxmox.ShutdownHost(ctx)
	if err != nil {
		e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Priority: notify.PriorityUrgent, Body: fmt.Sprintf("Host shutdown failed: %v", err)})
		return true, fmt.Errorf("engine: shut down host: %w", err)
	}
	if err := e.waitForTask(ctx, upid); err != nil {
		e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Priority: notify.PriorityUrgent, Body: fmt.Sprintf("Host shutdown failed: %v", err)})
		return true, err
	}

	e.Logger.Info("engine: shut down host")
	e.notifyBestEffort(ctx, notify.Notification{Title: "labpower", Body: "Shutting down the Proxmox host"})
	return true, e.recordEvent(ctx, "host_shutdown", hostTarget, "schedule", false)
}

func guestByName(guests []GuestConfig, name string) (GuestConfig, bool) {
	for _, g := range guests {
		if g.Name == name {
			return g, true
		}
	}
	return GuestConfig{}, false
}

func (e *Engine) getIntState(ctx context.Context, key string) (int, error) {
	s, ok, err := e.Store.GetState(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("engine: load %s: %w", key, err)
	}
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("engine: parse %s %q: %w", key, s, err)
	}
	return n, nil
}

// notifyBestEffort sends a notification without letting a delivery failure
// (ntfy being unreachable, say) block or fail the tick — notification is
// best-effort, scheduling correctness is not.
func (e *Engine) notifyBestEffort(ctx context.Context, n notify.Notification) {
	if err := e.Notifier.Notify(ctx, n); err != nil {
		e.Logger.Warn("engine: notification failed", "title", n.Title, "error", err)
	}
}
