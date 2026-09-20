// Package engine is the scheduler: it ticks periodically, decides what
// should change based on schedules and overrides, and drives guests and
// the host through internal/proxmox and internal/wol. It depends only on
// interfaces (clock, Proxmox, WoL, notify, weather, store) so a full week
// can be simulated in milliseconds — see internal/engine's tests and
// CLAUDE.md's "Scheduling engine" section.
//
// Weather ships notify-only in this milestone: Tick reports level changes
// as events and notifications, but nothing here gates a host or guest
// decision on it yet — that arrives with the weather-enforce milestone.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
	"github.com/Ultimatum22/powerwarden/internal/weather"
	"github.com/Ultimatum22/powerwarden/internal/wol"
)

// lastTickKey is the state table key holding the last tick's timestamp.
const lastTickKey = "last_tick"

// GuestConfig is the subset of a guest's config the engine needs to
// reconcile it: whether it's always_on, which named schedule governs it,
// and what it depends on for start/stop ordering.
type GuestConfig struct {
	Name      string
	AlwaysOn  bool
	Schedule  string
	DependsOn []string
}

// HostConfig is the subset of the host's config the engine needs.
type HostConfig struct {
	Schedule string
	// ShutdownGrace is parsed and validated but not yet used: per
	// CLAUDE.md's state machine notes, it's the short grace period that
	// lets a weather-Danger shutdown bypass the active-tasks check, which
	// arrives with the weather milestone.
	ShutdownGrace time.Duration
}

// Config configures a new Engine. See New.
type Config struct {
	Clock   clock.Clock
	Proxmox proxmox.Client
	Store   *store.Store
	Logger  *slog.Logger
	DryRun  bool
	Loc     *time.Location

	Guests    []GuestConfig
	Schedules map[string]schedule.Schedule
	Host      HostConfig

	WoLSender      wol.Sender
	WoLMAC         net.HardwareAddr
	WoLRetries     int
	WoLWakeTimeout time.Duration

	Notifier notify.Notifier

	// Weather is optional; nil disables weather monitoring entirely.
	Weather *weather.Monitor

	// TaskPollInterval and TaskTimeout control how long Tick waits for a
	// Proxmox task (start/shutdown) to finish before giving up. Zero means
	// use the defaults; tests shrink TaskPollInterval to keep the fake
	// client's (instant) completion from mattering either way.
	TaskPollInterval time.Duration
	TaskTimeout      time.Duration
}

// Engine ticks the scheduler. Construct with New so dependency ordering is
// validated and precomputed.
type Engine struct {
	Config
	order []string // guest names, dependencies before dependents
}

// New builds an Engine, computing and validating the guest dependency
// order up front so Tick never has to (and so a cycle is caught at
// construction rather than buried in a tick).
func New(cfg Config) (*Engine, error) {
	order, err := TopologicalOrder(cfg.Guests)
	if err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Notifier == nil {
		cfg.Notifier = notify.NewFake()
	}
	return &Engine{Config: cfg, order: order}, nil
}

// Tick runs one scheduler pass. It first reconciles the host (waking or
// shutting it down as needed — see host.go), then, only if the host is
// reachable and isn't shutting down this tick, reconciles guests the same
// way milestone 2 did: act only on what changed since the last tick (a
// schedule boundary crossed, or the active override changed) — see
// CLAUDE.md's "Edge-triggered with catch-up".
func (e *Engine) Tick(ctx context.Context) error {
	if !e.Clock.Trusted() {
		e.Logger.Warn("engine: clock not trusted yet, skipping tick")
		return nil
	}
	now := e.Clock.Now()

	e.reconcileWeather(ctx, now) // best-effort: notify-only, never blocks host/guest logic

	lastTickStr, hasLastTick, err := e.Store.GetState(ctx, lastTickKey)
	if err != nil {
		return fmt.Errorf("engine: load last_tick: %w", err)
	}
	bootstrap := !hasLastTick
	var lastTick time.Time
	if !bootstrap {
		lastTick, err = time.Parse(time.RFC3339, lastTickStr)
		if err != nil {
			return fmt.Errorf("engine: parse stored last_tick %q: %w", lastTickStr, err)
		}
	}

	_, reachErr := e.Proxmox.NodeStatus(ctx)
	reachable := reachErr == nil

	var actual map[string]proxmox.Guest
	if reachable {
		guests, err := e.Proxmox.ListGuests(ctx)
		if err != nil {
			return fmt.Errorf("engine: list guests: %w", err)
		}
		actual = make(map[string]proxmox.Guest, len(guests))
		for _, g := range guests {
			actual[g.Name] = g
		}
	}

	anyGuestWantsOn, err := e.anyGuestWantsOn(ctx, now)
	if err != nil {
		return err
	}

	shuttingDown, err := e.reconcileHost(ctx, now, reachable, anyGuestWantsOn, actual)
	if err != nil {
		return err
	}

	if !reachable || shuttingDown {
		// Guests can't be reconciled without the host, and while it's off
		// their schedule boundaries simply haven't been evaluated yet — so
		// last_tick must NOT advance here. If it did, a multi-day host-off
		// stretch would erase the catch-up window: once the host wakes
		// back up, plan() would see almost no elapsed time since
		// "last_tick" and miss boundaries that were crossed while the
		// host was down. Leaving it frozen means the very next tick where
		// guests actually get reconciled naturally sees the full gap and
		// catches up, the same as after any other downtime.
		return nil
	}

	starts, stops, err := e.plan(ctx, bootstrap, lastTick, now, actual)
	if err != nil {
		return err
	}

	var actionErrs []error
	// Stops first (dependents before their dependencies), then starts
	// (dependencies before their dependents) — see CLAUDE.md's "Guests"
	// section.
	for i := len(e.order) - 1; i >= 0; i-- {
		if a, ok := stops[e.order[i]]; ok {
			if err := e.execute(ctx, a); err != nil {
				actionErrs = append(actionErrs, err)
			}
		}
	}
	for _, name := range e.order {
		if a, ok := starts[name]; ok {
			if err := e.execute(ctx, a); err != nil {
				actionErrs = append(actionErrs, err)
			}
		}
	}
	if len(actionErrs) > 0 {
		return fmt.Errorf("engine: %d guest action(s) failed: %w", len(actionErrs), errors.Join(actionErrs...))
	}

	// Only checkpoint progress if everything this tick succeeded, so a
	// failure retries from the same boundary next tick instead of being
	// silently skipped.
	if err := e.Store.SetState(ctx, lastTickKey, now.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("engine: save last_tick: %w", err)
	}
	return nil
}
