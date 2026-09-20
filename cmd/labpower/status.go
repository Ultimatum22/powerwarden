package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"text/tabwriter"

	"os"

	"github.com/Ultimatum22/powerwarden/internal/config"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
)

func runStatus(ctx context.Context, args []string, logger *slog.Logger) error {
	fs, path := configFlagSet("status")
	if err := fs.Parse(args); err != nil {
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

	nodeStatus, nodeErr := client.NodeStatus(ctx)
	if nodeErr != nil {
		fmt.Printf("host %s: unreachable (%v)\n\n", cfg.Proxmox.Node, nodeErr)
		logger.Info("status: host unreachable", "actor", "cli", "node", cfg.Proxmox.Node, "error", nodeErr)
		return nil
	}
	fmt.Printf("host %s: reachable (uptime %ds)\n\n", nodeStatus.Node, nodeStatus.Uptime)

	guests, err := client.ListGuests(ctx)
	if err != nil {
		return fmt.Errorf("list guests: %w", err)
	}

	printGuestTable(guests, cfg)
	return nil
}

func printGuestTable(guests []proxmox.Guest, cfg *config.Config) {
	sort.Slice(guests, func(i, j int) bool { return guests[i].Name < guests[j].Name })

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVMID\tKIND\tSTATUS\tMANAGED AS")
	for _, g := range guests {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", g.Name, g.VMID, g.Kind, g.Status, managedAs(g.Name, cfg))
	}
	_ = tw.Flush()
}

func managedAs(name string, cfg *config.Config) string {
	guest, ok := cfg.Guests[name]
	if !ok {
		return "unmanaged"
	}
	if guest.AlwaysOn {
		return "always_on"
	}
	return "schedule:" + guest.Schedule
}
