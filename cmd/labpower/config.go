package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Ultimatum22/powerwarden/internal/config"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
)

const defaultConfigPath = "/etc/labpower/config.yaml"

// configFlagSet builds a FlagSet with the shared -config flag, for
// subcommands to add their own flags to before calling Parse.
func configFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	path := fs.String("config", defaultConfigPath, "path to config.yaml")
	return fs, path
}

func loadValidConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config is invalid:\n%w", err)
	}
	return cfg, nil
}

// newProxmoxClient builds the real HTTP client from a validated config,
// reading the token secret from disk (never from the config file itself).
func newProxmoxClient(cfg *config.Config) (proxmox.Client, error) {
	secret, err := os.ReadFile(cfg.Proxmox.TokenSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read proxmox token secret: %w", err)
	}
	client, err := proxmox.NewHTTPClient(
		cfg.Proxmox.URL,
		cfg.Proxmox.Node,
		cfg.Proxmox.TokenID,
		strings.TrimSpace(string(secret)),
		cfg.Proxmox.TLSFingerprint,
	)
	if err != nil {
		return nil, err
	}
	return client, nil
}
