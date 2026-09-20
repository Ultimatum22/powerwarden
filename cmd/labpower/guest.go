package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Ultimatum22/powerwarden/internal/proxmox"
)

const guestUsage = `Usage:
  labpower guest [-config path] start <name>
  labpower guest [-config path] stop <name>
`

func runGuest(ctx context.Context, args []string, logger *slog.Logger) error {
	fs, path := configFlagSet("guest")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Print(guestUsage)
		return errUsage
	}
	action, name := rest[0], rest[1]
	if action != "start" && action != "stop" {
		fmt.Print(guestUsage)
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}

	guestCfg, ok := cfg.Guests[name]
	if !ok {
		return fmt.Errorf("guest %q is not defined in config", name)
	}
	if guestCfg.AlwaysOn {
		return fmt.Errorf("guest %q is always_on and is never started or stopped by labpower", name)
	}

	client, err := newProxmoxClient(cfg)
	if err != nil {
		return err
	}

	guests, err := client.ListGuests(ctx)
	if err != nil {
		return fmt.Errorf("list guests: %w", err)
	}
	var target *proxmox.Guest
	for i := range guests {
		if guests[i].Name == name {
			target = &guests[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("guest %q not found on Proxmox cluster", name)
	}

	if cfg.IsDryRun() {
		fmt.Printf("dry-run: would %s guest %s (vmid %d, %s)\n", action, name, target.VMID, target.Kind)
		logger.Info("guest: dry-run", "actor", "cli", "dry_run", true, "action", action, "guest", name, "vmid", target.VMID)
		return nil
	}

	var upid proxmox.UPID
	if action == "start" {
		upid, err = client.StartGuest(ctx, target.Kind, target.VMID)
	} else {
		upid, err = client.ShutdownGuest(ctx, target.Kind, target.VMID)
	}
	if err != nil {
		logger.Error("guest: action failed", "actor", "cli", "dry_run", false, "action", action, "guest", name, "vmid", target.VMID, "error", err)
		return err
	}

	fmt.Printf("%s guest %s (vmid %d): task %s\n", action, name, target.VMID, upid)
	logger.Info("guest: action sent", "actor", "cli", "dry_run", false, "action", action, "guest", name, "vmid", target.VMID, "upid", string(upid))
	return nil
}
