package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validYAML(secretPath string) string {
	return `
dry_run: true
timezone: Europe/Amsterdam
listen: 127.0.0.1:8080
public_url: https://power.example.com
trusted_proxy: 127.0.0.1

proxmox:
  url: https://10.22.10.5:8006
  node: pve01
  token_id: labpower@pve!pi
  token_secret_file: ` + secretPath + `
  tls_fingerprint: "AB:CD:EF"

wol:
  method: unicast
  mac: "aa:bb:cc:dd:ee:ff"
  target: 10.22.10.250
  retries: 3
  wake_timeout: 5m

schedules:
  daytime:
    - { days: mon-fri, on: "07:00", off: "01:00" }
    - { days: sat-sun, on: "09:00", off: "01:30" }
  evenings:
    - { days: mon-sun, on: "17:00", off: "00:30" }

required_windows:
  - { name: trivy, days: mon, from: "05:30", to: "07:00" }

host:
  schedule: daytime
  shutdown_grace: 10m

guests:
  lxc-forge: { always_on: true }
  lxc-edge: { always_on: true }
  vm-media: { schedule: evenings, depends_on: [] }

notify:
  provider: ntfy
  url: https://ntfy.sh/labpower-test
`
}

func writeSecret(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "proxmox-token")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidConfigPasses(t *testing.T) {
	secret := writeSecret(t)
	c, err := Parse([]byte(validYAML(secret)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !c.IsDryRun() {
		t.Fatal("expected dry_run true")
	}
}

func TestDryRunMustBeExplicit(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret), "dry_run: true\n", "", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = c.Validate()
	if err == nil {
		t.Fatal("expected error for missing dry_run")
	}
	if !strings.Contains(err.Error(), "dry_run") {
		t.Fatalf("expected dry_run error, got: %v", err)
	}
}

func TestUnknownScheduleNameRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret), "schedule: evenings", "schedule: nonexistent", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("expected error mentioning unknown schedule, got: %v", err)
	}
}

func TestUnknownGuestDependencyRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret),
		"vm-media: { schedule: evenings, depends_on: [] }",
		"vm-media: { schedule: evenings, depends_on: [vm-ghost] }", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "vm-ghost") {
		t.Fatalf("expected error mentioning vm-ghost, got: %v", err)
	}
}

func TestDependencyCycleRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret),
		"vm-media: { schedule: evenings, depends_on: [] }\n",
		"vm-media: { schedule: evenings, depends_on: [] }\n"+
			"  vm-a: { schedule: evenings, depends_on: [vm-b] }\n"+
			"  vm-b: { schedule: evenings, depends_on: [vm-a] }\n", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestAlwaysOnWithScheduleRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret),
		"lxc-forge: { always_on: true }",
		"lxc-forge: { always_on: true, schedule: daytime }", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "always_on") {
		t.Fatalf("expected always_on conflict error, got: %v", err)
	}
}

func TestMissingSecretFileRejected(t *testing.T) {
	yaml := validYAML("/nonexistent/proxmox-token")
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "token_secret_file") {
		t.Fatalf("expected missing secret file error, got: %v", err)
	}
}

func TestNonLoopbackListenRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret), "listen: 127.0.0.1:8080", "listen: 0.0.0.0:8080", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback error, got: %v", err)
	}
}

func TestWoLMethodValidation(t *testing.T) {
	secret := writeSecret(t)
	tests := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{
			name: "unknown method",
			mutate: func(y string) string {
				return strings.Replace(y, "method: unicast", "method: carrier-pigeon", 1)
			},
			wantErr: "carrier-pigeon",
		},
		{
			name: "bad mac",
			mutate: func(y string) string {
				return strings.Replace(y, `mac: "aa:bb:cc:dd:ee:ff"`, `mac: "not-a-mac"`, 1)
			},
			wantErr: "wol.mac",
		},
		{
			name: "unicast without target",
			mutate: func(y string) string {
				return strings.Replace(y, "  target: 10.22.10.250\n", "", 1)
			},
			wantErr: "wol.target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := tt.mutate(validYAML(secret))
			c, err := Parse([]byte(yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	secret := writeSecret(t)
	yaml := validYAML(secret) + "\ntotally_unknown_field: true\n"
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected parse error for unknown field")
	}
}

func TestRouterAPIRequiresURL(t *testing.T) {
	secret := writeSecret(t)
	yaml := strings.Replace(validYAML(secret), "method: unicast", "method: router_api", 1)
	c, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "router_api.url") {
		t.Fatalf("expected router_api.url error, got: %v", err)
	}
}

// FuzzParse ensures the YAML parser never panics on arbitrary input, and
// that anything it does accept can be validated without panicking either.
func FuzzParse(f *testing.F) {
	dir := f.TempDir()
	secret := filepath.Join(dir, "proxmox-token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(validYAML(secret)))
	f.Add([]byte(""))
	f.Add([]byte("dry_run: true"))
	f.Add([]byte("guests: {a: {depends_on: [a]}}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data)
		if err != nil {
			return
		}
		_ = c.Validate()
	})
}
