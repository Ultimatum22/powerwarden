package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/wol"
)

func runWake(ctx context.Context, args []string, logger *slog.Logger) error {
	fs, path := configFlagSet("wake")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}

	mac, err := net.ParseMAC(cfg.WoL.MAC)
	if err != nil {
		return fmt.Errorf("wol.mac: %w", err)
	}

	if cfg.IsDryRun() {
		fmt.Printf("dry-run: would send Wake-on-LAN (%s) to %s\n", cfg.WoL.Method, mac)
		logger.Info("wake: dry-run", "actor", "cli", "dry_run", true, "method", cfg.WoL.Method, "mac", mac.String())
		return nil
	}

	sender, err := wol.New(cfg.WoL)
	if err != nil {
		return err
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := sender.Send(sendCtx, mac); err != nil {
		logger.Error("wake: send failed", "actor", "cli", "dry_run", false, "method", cfg.WoL.Method, "mac", mac.String(), "error", err)
		return err
	}

	fmt.Printf("sent Wake-on-LAN (%s) to %s\n", cfg.WoL.Method, mac)
	logger.Info("wake: sent", "actor", "cli", "dry_run", false, "method", cfg.WoL.Method, "mac", mac.String())
	return nil
}
