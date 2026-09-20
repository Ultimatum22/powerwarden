// Package engine is the scheduler: it ticks periodically, decides what
// should change based on schedules and overrides, and drives guests
// through internal/proxmox. It depends only on interfaces (clock, Proxmox,
// store) so a full week can be simulated in milliseconds — see
// internal/engine's tests and CLAUDE.md's "Scheduling engine" section.
//
// This milestone covers guest scheduling only; the host state machine and
// weather safeguard are built on top of the same Tick in later milestones.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
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

// Engine ticks the scheduler. Construct with New so dependency ordering is
// validated and precomputed.
type Engine struct {
	Clock   clock.Clock
	Proxmox proxmox.Client
	Store   *store.Store
	Logger  *slog.Logger
	DryRun  bool
	Loc     *time.Location

	Guests    []GuestConfig
	Schedules map[string]schedule.Schedule

	// TaskPollInterval and TaskTimeout control how long Tick waits for a
	// Proxmox task (start/shutdown) to finish before giving up. Zero means
	// use the defaults; tests shrink TaskPollInterval to keep the fake
	// client's (instant) completion from mattering either way.
	TaskPollInterval time.Duration
	TaskTimeout      time.Duration

	order []string // guest names, dependencies before dependents
}

// New builds an Engine, computing and validating the guest dependency
// order up front so Tick never has to (and so a cycle is caught at
// construction rather than buried in a tick).
func New(guests []GuestConfig, schedules map[string]schedule.Schedule, c clock.Clock, px proxmox.Client, st *store.Store, logger *slog.Logger, dryRun bool, loc *time.Location) (*Engine, error) {
	order, err := topologicalOrder(guests)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		Clock:     c,
		Proxmox:   px,
		Store:     st,
		Logger:    logger,
		DryRun:    dryRun,
		Loc:       loc,
		Guests:    guests,
		Schedules: schedules,
		order:     order,
	}, nil
}

// Tick runs one scheduler pass: it loads the last tick, computes what
// changed for each guest since then (a schedule boundary crossed, or the
// active override changed), and acts only on what changed — see
// CLAUDE.md's "Edge-triggered with catch-up".
func (e *Engine) Tick(ctx context.Context) error {
	if !e.Clock.Trusted() {
		e.Logger.Warn("engine: clock not trusted yet, skipping tick")
		return nil
	}
	now := e.Clock.Now()

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

	guests, err := e.Proxmox.ListGuests(ctx)
	if err != nil {
		return fmt.Errorf("engine: list guests: %w", err)
	}
	byName := make(map[string]proxmox.Guest, len(guests))
	for _, g := range guests {
		byName[g.Name] = g
	}

	starts, stops, err := e.plan(ctx, bootstrap, lastTick, now, byName)
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

	// Only checkpoint progress if every action this tick succeeded, so a
	// failure retries from the same boundary next tick instead of being
	// silently skipped (see plannedAction/execute).
	if len(actionErrs) > 0 {
		return fmt.Errorf("engine: %d action(s) failed: %w", len(actionErrs), errors.Join(actionErrs...))
	}
	if err := e.Store.SetState(ctx, lastTickKey, now.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("engine: save last_tick: %w", err)
	}
	return nil
}
