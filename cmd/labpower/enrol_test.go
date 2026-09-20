package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

func TestEnrolPrintsLinkAndIsUsableOnce(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)
	stateDir := t.TempDir()

	var out bytes.Buffer
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := run(context.Background(), []string{"enrol", "-config", cfgPath, "-state-dir", stateDir}, testLogger())
	w.Close()
	os.Stdout = origStdout
	out.ReadFrom(r)

	if err != nil {
		t.Fatalf("run enrol: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "/enrol?token=") {
		t.Fatalf("expected an enrolment link in output, got:\n%s", got)
	}
	if !strings.Contains(got, "https://power.example.com/enrol?token=") {
		t.Fatalf("expected the link to use public_url's host, got:\n%s", got)
	}
}

func TestEnrolRefusedAfterUserEnrolled(t *testing.T) {
	srv := fakeProxmoxServer(t)
	defer srv.Close()
	cfgPath := writeTestConfig(t, srv, true)
	stateDir := t.TempDir()

	if err := run(context.Background(), []string{"enrol", "-config", cfgPath, "-state-dir", stateDir}, testLogger()); err != nil {
		t.Fatalf("first enrol: %v", err)
	}

	// Simulate enrolment having completed (a user now exists).
	dbPath := filepath.Join(stateDir, "labpower.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(), store.User{ID: store.SoleUserID, Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := run(context.Background(), []string{"enrol", "-config", cfgPath, "-state-dir", stateDir}, testLogger()); err == nil {
		t.Fatal("expected enrol to refuse once a user already exists")
	}
}
