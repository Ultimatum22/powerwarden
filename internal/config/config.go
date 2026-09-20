// Package config loads and validates labpower's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"gopkg.in/yaml.v3"
)

// Config is the root of labpower's configuration file.
type Config struct {
	DryRun       *bool  `yaml:"dry_run"`
	Timezone     string `yaml:"timezone"`
	Listen       string `yaml:"listen"`
	PublicURL    string `yaml:"public_url"`
	TrustedProxy string `yaml:"trusted_proxy"`

	Proxmox Proxmox `yaml:"proxmox"`
	WoL     WoL     `yaml:"wol"`

	Schedules       map[string][]Window `yaml:"schedules"`
	RequiredWindows []RequiredWindow    `yaml:"required_windows"`
	Host            Host                `yaml:"host"`
	Guests          map[string]Guest    `yaml:"guests"`
	Weather         Weather             `yaml:"weather"`
	Notify          Notify              `yaml:"notify"`
	Auth            Auth                `yaml:"auth"`
}

// Proxmox holds connection details for the Proxmox VE API.
type Proxmox struct {
	URL             string `yaml:"url"`
	Node            string `yaml:"node"`
	TokenID         string `yaml:"token_id"`
	TokenSecretFile string `yaml:"token_secret_file"`
	TLSFingerprint  string `yaml:"tls_fingerprint"`
}

// WoL configures how Wake-on-LAN packets reach the Proxmox host.
type WoL struct {
	Method      string        `yaml:"method"` // router_api | unicast | broadcast
	MAC         string        `yaml:"mac"`
	Target      string        `yaml:"target"`
	Retries     int           `yaml:"retries"`
	WakeTimeout time.Duration `yaml:"wake_timeout"`
	RouterAPI   RouterAPI     `yaml:"router_api"`
}

// RouterAPI configures a generic templated HTTP call for wol.method: router_api.
// The exact API is TODO(owner) until the router/firewall model is known; the
// template fields let that be filled in later without a code change.
type RouterAPI struct {
	URL     string            `yaml:"url"`    // may contain {{.MAC}} / {{.IP}}
	Method  string            `yaml:"method"` // defaults to POST
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"` // may contain {{.MAC}} / {{.IP}}
}

// Window is a single on/off schedule entry.
type Window struct {
	Days string `yaml:"days"` // e.g. "mon-fri", "sat-sun", "mon-sun"
	On   string `yaml:"on"`   // HH:MM
	Off  string `yaml:"off"`  // HH:MM
}

// RequiredWindow is a period during which the host must be on, used only for
// validation warnings.
type RequiredWindow struct {
	Name string `yaml:"name"`
	Days string `yaml:"days"`
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Host configures the Proxmox host's own schedule.
type Host struct {
	Schedule      string        `yaml:"schedule"`
	ShutdownGrace time.Duration `yaml:"shutdown_grace"`
}

// Guest configures one scheduled or always-on VM/LXC.
type Guest struct {
	AlwaysOn  bool     `yaml:"always_on"`
	Schedule  string   `yaml:"schedule"`
	DependsOn []string `yaml:"depends_on"`
}

// Weather configures the storm safeguard. Full validation arrives with the
// weather milestone; for now the shape just needs to round-trip.
type Weather struct {
	Location struct {
		Lat float64 `yaml:"lat"`
		Lon float64 `yaml:"lon"`
	} `yaml:"location"`
	LightningNetwork struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"lightning_network"`
	LocalSensor struct {
		Enabled bool   `yaml:"enabled"`
		Bus     string `yaml:"bus"`
		Address uint8  `yaml:"address"`
		IRQGPIO int    `yaml:"irq_gpio"`
		// CorroborationWindow is how close together two local detections
		// must land to count as confirmed on their own, since the AS3935
		// is noisy (CLAUDE.md: "two local detections occur within 5
		// minutes... make this configurable"). Zero uses a 5-minute
		// default.
		CorroborationWindow time.Duration `yaml:"corroboration_window"`
	} `yaml:"local_sensor"`
	Warnings struct {
		Provider string `yaml:"provider"`
		Region   string `yaml:"region"`
	} `yaml:"warnings"`
	Forecast struct {
		Provider string        `yaml:"provider"`
		Interval time.Duration `yaml:"interval"`
	} `yaml:"forecast"`
	Levels struct {
		Warning struct {
			StrikeRadiusKM float64       `yaml:"strike_radius_km"`
			Countdown      time.Duration `yaml:"countdown"`
		} `yaml:"warning"`
		Danger struct {
			StrikeRadiusKM float64 `yaml:"strike_radius_km"`
		} `yaml:"danger"`
		AllClearAfter time.Duration `yaml:"all_clear_after"`
	} `yaml:"levels"`
	OnStaleData string        `yaml:"on_stale_data"`
	StaleAfter  time.Duration `yaml:"stale_after"`
	Mode        string        `yaml:"mode"` // notify | enforce, default notify
}

// Notify configures the outside-the-homelab notification service.
type Notify struct {
	Provider  string `yaml:"provider"`
	URL       string `yaml:"url"`
	TokenFile string `yaml:"token_file"`
}

// Auth configures WebAuthn and session behaviour.
type Auth struct {
	RPID            string        `yaml:"rp_id"`
	SessionIdle     time.Duration `yaml:"session_idle"`
	SessionAbsolute time.Duration `yaml:"session_absolute"`
}

// Load reads and parses the YAML file at path. It does not validate; call
// Validate on the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes into a Config. It rejects unknown fields so typos
// in the config fail loudly instead of being silently ignored.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// IsDryRun reports the configured dry_run value. Callers must only use this
// after Validate has confirmed DryRun is not nil.
func (c *Config) IsDryRun() bool {
	return c.DryRun != nil && *c.DryRun
}

// Validate checks the configuration for internal consistency. It returns a
// single error joining every problem found, so the caller can report them
// all at once (see check-config).
func (c *Config) Validate() error {
	var errs []error
	check := func(cond bool, format string, args ...any) {
		if !cond {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	check(c.DryRun != nil, "dry_run must be set explicitly (true or false)")
	check(c.Timezone != "", "timezone is required")
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("timezone %q is not a valid IANA zone: %w", c.Timezone, err))
		}
	}
	check(c.Listen != "", "listen is required")
	if c.Listen != "" {
		if err := validateLoopbackListen(c.Listen); err != nil {
			errs = append(errs, err)
		}
	}
	if c.TrustedProxy != "" {
		check(net.ParseIP(c.TrustedProxy) != nil, "trusted_proxy %q is not a valid IP", c.TrustedProxy)
	}

	errs = append(errs, c.validateProxmox()...)
	errs = append(errs, c.validateWoL()...)
	errs = append(errs, c.validateSchedules()...)
	errs = append(errs, c.validateHost()...)
	errs = append(errs, c.validateGuests()...)
	errs = append(errs, c.validateNotify()...)
	errs = append(errs, c.validateWeather()...)
	errs = append(errs, c.validateAuth()...)

	return joinNonNil(errs)
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen %q must be host:port: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen %q must bind a loopback address (labpower must not be reachable except via Newt)", listen)
	}
	return nil
}

func (c *Config) validateProxmox() []error {
	var errs []error
	check := func(cond bool, format string, args ...any) {
		if !cond {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(c.Proxmox.URL != "", "proxmox.url is required")
	if c.Proxmox.URL != "" {
		u, err := url.Parse(c.Proxmox.URL)
		if err != nil || u.Scheme != "https" {
			errs = append(errs, fmt.Errorf("proxmox.url %q must be a valid https URL", c.Proxmox.URL))
		}
	}
	check(c.Proxmox.Node != "", "proxmox.node is required")
	check(c.Proxmox.TokenID != "", "proxmox.token_id is required")
	check(c.Proxmox.TLSFingerprint != "", "proxmox.tls_fingerprint is required (never skip TLS verification)")
	if c.Proxmox.TokenSecretFile == "" {
		errs = append(errs, fmt.Errorf("proxmox.token_secret_file is required"))
	} else if err := fileExists(c.Proxmox.TokenSecretFile); err != nil {
		errs = append(errs, fmt.Errorf("proxmox.token_secret_file: %w", err))
	}
	return errs
}

func (c *Config) validateNotify() []error {
	var errs []error
	switch c.Notify.Provider {
	case "ntfy":
	case "":
		errs = append(errs, fmt.Errorf("notify.provider is required (currently only \"ntfy\" is supported)"))
	default:
		errs = append(errs, fmt.Errorf("notify.provider %q is not supported (currently only \"ntfy\" is)", c.Notify.Provider))
	}
	if c.Notify.URL == "" {
		errs = append(errs, fmt.Errorf("notify.url is required"))
	} else if u, err := url.Parse(c.Notify.URL); err != nil || u.Scheme != "https" {
		errs = append(errs, fmt.Errorf("notify.url %q must be a valid https URL (a notifier outside the homelab)", c.Notify.URL))
	}
	// notify.token_file is optional: some ntfy topics are unauthenticated.
	if c.Notify.TokenFile != "" {
		if err := fileExists(c.Notify.TokenFile); err != nil {
			errs = append(errs, fmt.Errorf("notify.token_file: %w", err))
		}
	}
	return errs
}

func (c *Config) validateWeather() []error {
	var errs []error
	switch c.Weather.Mode {
	case "", "notify", "enforce":
	default:
		errs = append(errs, fmt.Errorf("weather.mode %q must be \"notify\" or \"enforce\"", c.Weather.Mode))
	}

	if c.Weather.Warnings.Provider != "" && c.Weather.Warnings.Provider != "meteoalarm" {
		errs = append(errs, fmt.Errorf("weather.warnings.provider %q is not supported (currently only \"meteoalarm\" is)", c.Weather.Warnings.Provider))
	}
	if c.Weather.Forecast.Provider != "" && c.Weather.Forecast.Provider != "open-meteo" {
		errs = append(errs, fmt.Errorf("weather.forecast.provider %q is not supported (currently only \"open-meteo\" is)", c.Weather.Forecast.Provider))
	}

	if c.Weather.LightningNetwork.Enabled {
		warn, danger := c.Weather.Levels.Warning.StrikeRadiusKM, c.Weather.Levels.Danger.StrikeRadiusKM
		if warn <= 0 {
			errs = append(errs, fmt.Errorf("weather.levels.warning.strike_radius_km must be positive when lightning_network is enabled"))
		}
		if danger <= 0 {
			errs = append(errs, fmt.Errorf("weather.levels.danger.strike_radius_km must be positive when lightning_network is enabled"))
		}
		if warn > 0 && danger > 0 && danger > warn {
			errs = append(errs, fmt.Errorf("weather.levels.danger.strike_radius_km (%.1f) must not exceed warning.strike_radius_km (%.1f)", danger, warn))
		}
	}

	if c.Weather.LocalSensor.Enabled {
		if c.Weather.LocalSensor.Bus == "" {
			errs = append(errs, fmt.Errorf("weather.local_sensor.bus is required when local_sensor is enabled"))
		}
		if c.Weather.LocalSensor.IRQGPIO <= 0 {
			errs = append(errs, fmt.Errorf("weather.local_sensor.irq_gpio is required when local_sensor is enabled"))
		}
	}

	if c.Weather.Mode == "enforce" {
		if c.Weather.Levels.Warning.Countdown <= 0 {
			errs = append(errs, fmt.Errorf("weather.levels.warning.countdown must be positive when weather.mode is \"enforce\" (an unset countdown would shut down instantly on Warning)"))
		}
		if c.Host.ShutdownGrace <= 0 {
			errs = append(errs, fmt.Errorf("host.shutdown_grace must be positive when weather.mode is \"enforce\" (it governs how long a Danger shutdown waits for active tasks)"))
		}
	}
	return errs
}

func (c *Config) validateAuth() []error {
	var errs []error
	if c.Auth.RPID == "" {
		errs = append(errs, fmt.Errorf("auth.rp_id is required (the WebAuthn relying party ID)"))
	}
	if c.Auth.SessionIdle <= 0 {
		errs = append(errs, fmt.Errorf("auth.session_idle must be positive"))
	}
	if c.Auth.SessionAbsolute <= 0 {
		errs = append(errs, fmt.Errorf("auth.session_absolute must be positive"))
	}
	if c.Auth.SessionIdle > 0 && c.Auth.SessionAbsolute > 0 && c.Auth.SessionIdle > c.Auth.SessionAbsolute {
		errs = append(errs, fmt.Errorf("auth.session_idle must not exceed auth.session_absolute"))
	}
	return errs
}

func (c *Config) validateWoL() []error {
	var errs []error
	switch c.WoL.Method {
	case "router_api", "unicast", "broadcast":
	case "":
		errs = append(errs, fmt.Errorf("wol.method is required (router_api, unicast, or broadcast)"))
	default:
		errs = append(errs, fmt.Errorf("wol.method %q must be router_api, unicast, or broadcast", c.WoL.Method))
	}
	if c.WoL.MAC == "" {
		errs = append(errs, fmt.Errorf("wol.mac is required"))
	} else if _, err := net.ParseMAC(c.WoL.MAC); err != nil {
		errs = append(errs, fmt.Errorf("wol.mac %q is invalid: %w", c.WoL.MAC, err))
	}
	switch c.WoL.Method {
	case "unicast", "broadcast":
		check := c.WoL.Target == ""
		if check {
			errs = append(errs, fmt.Errorf("wol.target is required for method %q", c.WoL.Method))
		} else if net.ParseIP(c.WoL.Target) == nil {
			errs = append(errs, fmt.Errorf("wol.target %q is not a valid IP", c.WoL.Target))
		}
	case "router_api":
		if c.WoL.RouterAPI.URL == "" {
			errs = append(errs, fmt.Errorf("wol.router_api.url is required for method \"router_api\""))
		}
	}
	if c.WoL.Retries < 0 {
		errs = append(errs, fmt.Errorf("wol.retries must not be negative"))
	}
	return errs
}

func (c *Config) validateSchedules() []error {
	var errs []error
	for name, windows := range c.Schedules {
		if len(windows) == 0 {
			errs = append(errs, fmt.Errorf("schedules.%s has no windows", name))
		}
		for i, w := range windows {
			if _, err := schedule.NewWindow(w.Days, w.On, w.Off); err != nil {
				errs = append(errs, fmt.Errorf("schedules.%s[%d]: %w", name, i, err))
			}
		}
	}
	for i, rw := range c.RequiredWindows {
		if rw.Name == "" {
			errs = append(errs, fmt.Errorf("required_windows[%d].name is required", i))
		}
		if _, err := schedule.ParseDays(rw.Days); err != nil {
			errs = append(errs, fmt.Errorf("required_windows[%d]: %w", i, err))
		}
		if _, err := schedule.ParseTimeOfDay(rw.From); err != nil {
			errs = append(errs, fmt.Errorf("required_windows[%d].from: %w", i, err))
		}
		if _, err := schedule.ParseTimeOfDay(rw.To); err != nil {
			errs = append(errs, fmt.Errorf("required_windows[%d].to: %w", i, err))
		}
	}
	// Whether each required_window actually falls inside the host's
	// schedule is a cross-check the engine performs at load time
	// (milestone 4, once the host state machine exists), not here.
	return errs
}

func (c *Config) validateHost() []error {
	var errs []error
	if c.Host.Schedule == "" {
		errs = append(errs, fmt.Errorf("host.schedule is required"))
	} else if _, ok := c.Schedules[c.Host.Schedule]; !ok {
		errs = append(errs, fmt.Errorf("host.schedule %q is not defined in schedules", c.Host.Schedule))
	}
	if c.Host.ShutdownGrace < 0 {
		errs = append(errs, fmt.Errorf("host.shutdown_grace must not be negative"))
	}
	return errs
}

func (c *Config) validateGuests() []error {
	var errs []error
	for name, g := range c.Guests {
		if g.AlwaysOn && g.Schedule != "" {
			errs = append(errs, fmt.Errorf("guests.%s: always_on cannot be combined with a schedule", name))
		}
		if !g.AlwaysOn {
			if g.Schedule == "" {
				errs = append(errs, fmt.Errorf("guests.%s: schedule is required unless always_on", name))
			} else if _, ok := c.Schedules[g.Schedule]; !ok {
				errs = append(errs, fmt.Errorf("guests.%s: schedule %q is not defined in schedules", name, g.Schedule))
			}
		}
		for _, dep := range g.DependsOn {
			if _, ok := c.Guests[dep]; !ok {
				errs = append(errs, fmt.Errorf("guests.%s: depends_on %q is not a defined guest", name, dep))
			}
		}
	}
	if cycle := findDependencyCycle(c.Guests); cycle != "" {
		errs = append(errs, fmt.Errorf("guests: dependency cycle detected: %s", cycle))
	}
	return errs
}

// findDependencyCycle returns a description of the first cycle found in the
// guests' depends_on graph, or "" if the graph is acyclic.
func findDependencyCycle(guests map[string]Guest) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(guests))
	var path []string

	var visit func(name string) string
	visit = func(name string) string {
		color[name] = gray
		path = append(path, name)
		for _, dep := range guests[name].DependsOn {
			switch color[dep] {
			case gray:
				return strings.Join(append(path, dep), " -> ")
			case white:
				if _, ok := guests[dep]; ok {
					if cycle := visit(dep); cycle != "" {
						return cycle
					}
				}
			}
		}
		path = path[:len(path)-1]
		color[name] = black
		return ""
	}

	names := make([]string, 0, len(guests))
	for name := range guests {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if color[name] == white {
			if cycle := visit(name); cycle != "" {
				return cycle
			}
		}
	}
	return ""
}

func fileExists(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", path)
	}
	return nil
}

func joinNonNil(errs []error) error {
	return errors.Join(errs...)
}
