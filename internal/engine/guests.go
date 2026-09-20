package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// plannedAction is one guest start or shutdown this tick decided to make.
type plannedAction struct {
	guest    GuestConfig
	vmid     int
	kind     proxmox.GuestKind
	start    bool // true: start, false: shutdown
	override *store.Override
}

// plan decides, for every non-always-on guest, whether this tick needs to
// act on it, per CLAUDE.md's edge-triggered rule: act only when a schedule
// boundary was crossed since lastTick, or when the effective override
// changed (started, changed, or expired) — never just because the guest
// currently disagrees with the schedule (that's how a guest started
// manually in the Proxmox UI stays on until the next boundary, instead of
// being shut down on the very next tick).
func (e *Engine) plan(ctx context.Context, bootstrap bool, lastTick, now time.Time, actual map[string]proxmox.Guest) (starts, stops map[string]plannedAction, err error) {
	starts = make(map[string]plannedAction)
	stops = make(map[string]plannedAction)

	for _, g := range e.Guests {
		if g.AlwaysOn {
			continue
		}
		sched := e.Schedules[g.Schedule]

		overrideNow, err := e.Store.EffectiveOverride(ctx, g.Name, now)
		if err != nil {
			return nil, nil, fmt.Errorf("engine: effective override for %q: %w", g.Name, err)
		}

		trigger := bootstrap
		if !trigger {
			if len(sched.Crossings(lastTick, now, e.Loc)) > 0 {
				trigger = true
			} else {
				overrideAtLastTick, err := e.Store.EffectiveOverride(ctx, g.Name, lastTick)
				if err != nil {
					return nil, nil, fmt.Errorf("engine: effective override for %q at last tick: %w", g.Name, err)
				}
				trigger = !overridesEqual(overrideAtLastTick, overrideNow)
			}
		}
		if !trigger {
			continue
		}

		desired, act := desiredState(overrideNow, sched, now, e.Loc)
		if !act {
			continue // paused: leave the guest exactly as it is
		}

		guest, found := actual[g.Name]
		if !found {
			e.Logger.Warn("engine: guest not found on Proxmox cluster", "guest", g.Name)
			continue
		}
		actualOn := guest.Status == proxmox.StatusRunning
		if actualOn == desired {
			continue // already in the desired state
		}

		a := plannedAction{guest: g, vmid: guest.VMID, kind: guest.Kind, start: desired, override: overrideNow}
		if desired {
			starts[g.Name] = a
		} else {
			stops[g.Name] = a
		}
	}
	return starts, stops, nil
}

// desiredState computes what a guest's state should be right now, and
// whether the engine should act on it at all (a "pause" override means
// "leave it alone").
func desiredState(ov *store.Override, sched schedule.Schedule, now time.Time, loc *time.Location) (desired, act bool) {
	if ov != nil {
		switch ov.Action {
		case "on":
			return true, true
		case "off":
			return false, true
		case "pause":
			return false, false
		}
		// Any other action (e.g. ignore_weather) doesn't govern guest
		// on/off state; fall through to the schedule.
	}
	return sched.IsOn(now, loc), true
}

// overridesEqual reports whether a and b are the same override row. Since
// "active at t" is computed from created_at/until/cancelled_at rather than
// a mutable flag, comparing by ID is sufficient: if the same row is active
// at two instants, nothing meaningful changed between them.
func overridesEqual(a, b *store.Override) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID
}

// execute carries out a planned action: in dry-run it only logs and
// records the event; live, it calls Proxmox and waits for the task before
// returning, per CLAUDE.md ("wait for the task (UPID) to finish before
// continuing").
func (e *Engine) execute(ctx context.Context, a plannedAction) error {
	actor := "schedule"
	if a.override != nil {
		actor = a.override.CreatedBy
	}
	verb, kind := "shutdown", "guest_shutdown"
	if a.start {
		verb, kind = "start", "guest_start"
	}

	if e.DryRun {
		e.Logger.Info("dry-run: would "+verb+" guest", "guest", a.guest.Name, "vmid", a.vmid, "actor", actor)
		return e.recordEvent(ctx, kind, a.guest.Name, actor, true)
	}

	var upid proxmox.UPID
	var err error
	if a.start {
		upid, err = e.Proxmox.StartGuest(ctx, a.kind, a.vmid)
	} else {
		upid, err = e.Proxmox.ShutdownGuest(ctx, a.kind, a.vmid)
	}
	if err != nil {
		e.Logger.Error("engine: "+verb+" guest failed", "guest", a.guest.Name, "vmid", a.vmid, "error", err)
		return fmt.Errorf("engine: %s guest %q: %w", verb, a.guest.Name, err)
	}
	if err := e.waitForTask(ctx, upid); err != nil {
		e.Logger.Error("engine: waiting for task failed", "guest", a.guest.Name, "upid", upid, "error", err)
		return err
	}

	e.Logger.Info(verb+" guest", "guest", a.guest.Name, "vmid", a.vmid, "actor", actor)
	return e.recordEvent(ctx, kind, a.guest.Name, actor, false)
}

func (e *Engine) recordEvent(ctx context.Context, kind, target, actor string, dryRun bool) error {
	return e.Store.RecordEvent(ctx, store.Event{
		At: e.Clock.Now(), Kind: kind, Target: target, Actor: actor, DryRun: dryRun,
	})
}

const (
	defaultTaskPollInterval = 2 * time.Second
	defaultTaskTimeout      = 2 * time.Minute
)

// waitForTask polls until upid finishes or times out. It uses real wall
// time, not the injected Clock: Clock governs scheduling decisions, while
// this is an operational wait on the actual Proxmox job. Against the fake
// client used in tests, tasks are already complete on the first poll, so
// this never actually sleeps in a test run.
func (e *Engine) waitForTask(ctx context.Context, upid proxmox.UPID) error {
	pollInterval := e.TaskPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultTaskPollInterval
	}
	timeout := e.TaskTimeout
	if timeout <= 0 {
		timeout = defaultTaskTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		status, err := e.Proxmox.TaskStatus(ctx, upid)
		if err != nil {
			return fmt.Errorf("engine: check task %s: %w", upid, err)
		}
		switch status.State {
		case proxmox.TaskOK:
			return nil
		case proxmox.TaskError:
			return fmt.Errorf("engine: task %s failed: %s", upid, status.ExitStatus)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("engine: task %s did not complete within %s", upid, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// topologicalOrder returns guest names ordered so every guest appears
// after everything it depends_on. Config validation already rejects
// cycles, but this is re-checked here so Engine can be constructed
// directly (e.g. in tests) without going through config.
func topologicalOrder(guests []GuestConfig) ([]string, error) {
	byName := make(map[string]GuestConfig, len(guests))
	for _, g := range guests {
		byName[g.Name] = g
	}

	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(guests))
	order := make([]string, 0, len(guests))

	var visit func(name string) error
	visit = func(name string) error {
		color[name] = gray
		for _, dep := range byName[name].DependsOn {
			switch color[dep] {
			case gray:
				return fmt.Errorf("engine: dependency cycle involving %q", name)
			case white:
				if _, ok := byName[dep]; ok {
					if err := visit(dep); err != nil {
						return err
					}
				}
			}
		}
		color[name] = black
		order = append(order, name)
		return nil
	}

	names := make([]string, 0, len(guests))
	for _, g := range guests {
		names = append(names, g.Name)
	}
	sort.Strings(names) // deterministic across runs

	for _, name := range names {
		if color[name] == white {
			if err := visit(name); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}
