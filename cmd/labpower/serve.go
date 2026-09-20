package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/config"
	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/sensor/as3935"
	"github.com/Ultimatum22/powerwarden/internal/store"
	"github.com/Ultimatum22/powerwarden/internal/weather"
	"github.com/Ultimatum22/powerwarden/internal/wol"
)

// tickInterval matches CLAUDE.md's "the engine ticks every 30 seconds".
const tickInterval = 30 * time.Second

// eventRetention matches CLAUDE.md's "prune events older than 90 days daily".
const eventRetention = 90 * 24 * time.Hour

func runServe(ctx context.Context, args []string, logger *slog.Logger) error {
	fs, path := configFlagSet("serve")
	stateDir := fs.String("state-dir", defaultStateDir(), "directory for labpower.db (defaults to $STATE_DIRECTORY, or /var/lib/labpower)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}

	proxmoxClient, err := newProxmoxClient(cfg)
	if err != nil {
		return err
	}

	dbPath := filepath.Join(*stateDir, "labpower.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store at %s: %w", dbPath, err)
	}
	defer st.Close()

	guests, schedules, err := engineInputsFromConfig(cfg)
	if err != nil {
		return err
	}

	mac, err := net.ParseMAC(cfg.WoL.MAC)
	if err != nil {
		return fmt.Errorf("wol.mac: %w", err)
	}
	wolSender, err := wol.New(cfg.WoL)
	if err != nil {
		return err
	}
	notifier, err := newNotifier(cfg)
	if err != nil {
		return err
	}

	weatherCfg := newWeatherMonitorConfig(cfg, logger)
	weatherMonitor := weather.NewMonitor(weatherCfg, clock.Real{}.Now())
	if watcher, ok := weatherCfg.Local.(*as3935.Watcher); ok {
		go watcher.Run(ctx)
	}

	eng, err := engine.New(engine.Config{
		Clock:   clock.Real{},
		Proxmox: proxmoxClient,
		Store:   st,
		Logger:  logger,
		DryRun:  cfg.IsDryRun(),
		Loc:     loc,

		Guests:    guests,
		Schedules: schedules,
		Host: engine.HostConfig{
			Schedule:      cfg.Host.Schedule,
			ShutdownGrace: cfg.Host.ShutdownGrace,
		},

		WoLSender:      wolSender,
		WoLMAC:         mac,
		WoLRetries:     cfg.WoL.Retries,
		WoLWakeTimeout: cfg.WoL.WakeTimeout,

		Notifier: notifier,
		Weather:  weatherMonitor,
	})
	if err != nil {
		return err
	}

	logger.Info("labpower serve starting", "dry_run", cfg.IsDryRun(), "state_dir", *stateDir, "guests", len(guests))

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	lastPruneDay := -1
	tick := func() {
		if err := eng.Tick(ctx); err != nil {
			logger.Error("engine tick failed", "error", err)
		}
		now := clock.Real{}.Now()
		if now.YearDay() != lastPruneDay {
			if err := st.PruneEventsOlderThan(ctx, now.Add(-eventRetention)); err != nil {
				logger.Error("event pruning failed", "error", err)
			}
			lastPruneDay = now.YearDay()
		}
	}

	tick() // don't wait a full interval before the first tick
	for {
		select {
		case <-ctx.Done():
			logger.Info("labpower serve shutting down")
			return nil
		case <-ticker.C:
			tick()
		}
	}
}

// newNotifier builds the configured Notifier, reading its token from disk
// (never from the config file itself).
func newNotifier(cfg *config.Config) (notify.Notifier, error) {
	var token string
	if cfg.Notify.TokenFile != "" {
		b, err := os.ReadFile(cfg.Notify.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read notify token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	return notify.New(cfg.Notify, token)
}

// defaultStateDir follows systemd's StateDirectory= convention: when the
// unit sets StateDirectory=labpower, systemd exports $STATE_DIRECTORY
// pointing at it. Locally (no systemd), fall back to a fixed path.
func defaultStateDir() string {
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		return dir
	}
	return "/var/lib/labpower"
}

// engineInputsFromConfig converts the validated config's schedules and
// guests into the plain types internal/engine operates on, keeping the
// engine package itself free of any dependency on internal/config.
func engineInputsFromConfig(cfg *config.Config) ([]engine.GuestConfig, map[string]schedule.Schedule, error) {
	schedules := make(map[string]schedule.Schedule, len(cfg.Schedules))
	for name, windows := range cfg.Schedules {
		var sched schedule.Schedule
		for _, w := range windows {
			win, err := schedule.NewWindow(w.Days, w.On, w.Off)
			if err != nil {
				return nil, nil, fmt.Errorf("schedules.%s: %w", name, err)
			}
			sched = append(sched, win)
		}
		schedules[name] = sched
	}

	guests := make([]engine.GuestConfig, 0, len(cfg.Guests))
	for name, g := range cfg.Guests {
		guests = append(guests, engine.GuestConfig{
			Name:      name,
			AlwaysOn:  g.AlwaysOn,
			Schedule:  g.Schedule,
			DependsOn: g.DependsOn,
		})
	}
	return guests, schedules, nil
}
