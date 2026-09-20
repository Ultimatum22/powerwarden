// Command labpower is the LabyrinthStack power manager: it starts and stops
// Proxmox guests on a schedule, wakes/shuts down the host, and (in later
// milestones) shuts the host down ahead of thunderstorms and serves a web
// UI for manual control. See CLAUDE.md for the full design.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // embed the IANA database so timezones work on any OS image
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], logger); err != nil {
		if !errors.Is(err, errUsage) {
			logger.Error(err.Error())
		}
		os.Exit(1)
	}
}

// errUsage marks an error whose usage text has already been printed, so
// main doesn't also log it as an "error".
var errUsage = errors.New("usage error")

const topLevelUsage = `labpower manages power for the LabyrinthStack homelab.

Usage:
  labpower <command> [arguments]

Commands:
  check-config          Load and validate the config file
  status                Show host and guest status
  wake                  Send a Wake-on-LAN packet to the host
  guest start <name>    Start a guest now
  guest stop <name>     Stop a guest now
  serve                 Run the guest scheduler (web UI not yet implemented)
  host shutdown         Shut down the Proxmox host (not yet implemented)
  enrol                 Print a first-run enrolment token (not yet implemented)

Global flags (place after the command):
  -config path   Path to config.yaml (default: /etc/labpower/config.yaml)
`

func run(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, topLevelUsage)
		return errUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "check-config":
		return runCheckConfig(rest)
	case "status":
		return runStatus(ctx, rest, logger)
	case "wake":
		return runWake(ctx, rest, logger)
	case "guest":
		return runGuest(ctx, rest, logger)
	case "serve":
		return runServe(ctx, rest, logger)
	case "host", "enrol":
		fmt.Fprintf(os.Stderr, "labpower %s: not implemented yet (planned for a later milestone; see CLAUDE.md)\n", cmd)
		return errUsage
	case "-h", "-help", "--help", "help":
		fmt.Fprint(os.Stderr, topLevelUsage)
		return nil
	case "-version", "--version", "version":
		fmt.Println(version)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "labpower: unknown command %q\n\n%s", cmd, topLevelUsage)
		return errUsage
	}
}
