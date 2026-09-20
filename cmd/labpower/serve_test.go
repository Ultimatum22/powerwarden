package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeTicksAndShutsDownCleanly(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true) // dry-run: never touches the mutating endpoints
	stateDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := run(ctx, []string{"serve", "-config", cfgPath, "-state-dir", stateDir}, testLogger())
	if err != nil {
		t.Fatalf("run serve: %v", err)
	}

	if _, err := os.Stat(filepath.Join(stateDir, "labpower.db")); err != nil {
		t.Fatalf("expected labpower.db to be created in state-dir: %v", err)
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(badPath, []byte("dry_run: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := run(ctx, []string{"serve", "-config", badPath, "-state-dir", dir}, testLogger()); err == nil {
		t.Fatal("expected serve to reject an invalid config before starting the loop")
	}
}
