package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/Ultimatum22/powerwarden/internal/config"
	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
)

const hostUsage = `Usage:
  labpower host shutdown [-config path] [-yes]
`

func runHost(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 || args[0] != "shutdown" {
		fmt.Print(hostUsage)
		return errUsage
	}

	fs, path := configFlagSet("host shutdown")
	yes := fs.Bool("yes", false, "actually shut down (without this, only prints what would happen)")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}
	client, err := newProxmoxClient(cfg)
	if err != nil {
		return err
	}

	guests, err := client.ListGuests(ctx)
	if err != nil {
		return fmt.Errorf("list guests: %w", err)
	}
	byName := make(map[string]proxmox.Guest, len(guests))
	for _, g := range guests {
		byName[g.Name] = g
	}

	guestConfigs, _, err := engineInputsFromConfig(cfg)
	if err != nil {
		return err
	}
	order, err := engine.TopologicalOrder(guestConfigs)
	if err != nil {
		return err
	}

	tasks, err := client.ActiveTasks(ctx)
	if err != nil {
		return fmt.Errorf("check active tasks: %w", err)
	}
	if len(tasks) > 0 {
		return fmt.Errorf("refusing to shut down: %d task(s) still active on the node (check the Proxmox UI)", len(tasks))
	}

	printShutdownSummary(cfg, byName)

	if !*yes {
		fmt.Println("\nPass -yes to actually shut down.")
		return nil
	}
	if cfg.IsDryRun() {
		fmt.Println("\ndry-run: would shut down the guests above, then the host")
		logger.Info("host shutdown: dry-run", "actor", "cli", "dry_run", true)
		return nil
	}

	for i := len(order) - 1; i >= 0; i-- {
		g, ok := byName[order[i]]
		if !ok || g.Status != proxmox.StatusRunning {
			continue
		}
		guestCfg := cfg.Guests[order[i]]
		if guestCfg.AlwaysOn {
			continue
		}
		fmt.Printf("stopping %s (vmid %d)...\n", g.Name, g.VMID)
		if _, err := client.ShutdownGuest(ctx, g.Kind, g.VMID); err != nil {
			return fmt.Errorf("stop guest %q: %w", g.Name, err)
		}
		logger.Info("host shutdown: stopped guest", "actor", "cli", "guest", g.Name, "vmid", g.VMID)
	}

	fmt.Println("shutting down host...")
	upid, err := client.ShutdownHost(ctx)
	if err != nil {
		logger.Error("host shutdown: failed", "actor", "cli", "error", err)
		return fmt.Errorf("shut down host: %w", err)
	}
	logger.Info("host shutdown: initiated", "actor", "cli", "upid", string(upid))
	fmt.Printf("host shutdown initiated: task %s\n", upid)
	return nil
}

func printShutdownSummary(cfg *config.Config, byName map[string]proxmox.Guest) {
	fmt.Println("What goes down:")
	names := make([]string, 0, len(cfg.Guests))
	for name := range cfg.Guests {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		g, found := byName[name]
		guestCfg := cfg.Guests[name]
		status := "not found on cluster"
		if found {
			status = string(g.Status)
		}
		if guestCfg.AlwaysOn {
			fmt.Printf("  %-20s ALWAYS ON — will be taken down with the host (%s)\n", name, status)
		} else {
			fmt.Printf("  %-20s %s\n", name, status)
		}
	}
}
