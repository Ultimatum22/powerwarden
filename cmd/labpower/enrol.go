package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// runEnrol prints a one-time enrolment link to the console, per
// CLAUDE.md: "a one-time enrolment token printed by labpower enrol on the
// Pi's console, valid 15 minutes. No default credentials, ever."
func runEnrol(ctx context.Context, args []string, logger *slog.Logger) error {
	fs, path := configFlagSet("enrol")
	stateDir := fs.String("state-dir", defaultStateDir(), "directory holding labpower.db")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}

	dbPath := filepath.Join(*stateDir, "labpower.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store at %s: %w", dbPath, err)
	}
	defer st.Close()

	now := clock.Real{}.Now()
	token, err := auth.GenerateEnrolToken(ctx, st, now)
	if err != nil {
		return err
	}

	link := fmt.Sprintf("https://%s/enrol?token=%s", publicHostname(cfg.PublicURL), token)
	fmt.Printf("Enrolment link (valid %s, single use):\n\n  %s\n\n", auth.EnrolTokenTTL, link)
	logger.Info("enrol: token generated", "actor", "cli", "expires", now.Add(auth.EnrolTokenTTL).Format(time.RFC3339))
	return nil
}
