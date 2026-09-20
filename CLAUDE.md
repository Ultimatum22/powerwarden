# CLAUDE.md

This file provides guidance to Claude Code when working in the **labpower** repository.

## Project overview

**labpower** is a small power manager for the LabyrinthStack homelab. It runs 24/7 on a Raspberry Pi Zero 2 W (outside the Proxmox server) and:

1. Starts and shuts down Proxmox VMs/LXCs on per-guest schedules.
2. Lets the owner override schedules from a web UI (start now, keep on until, stop, pause, vacation mode).
3. Shuts down the whole Proxmox host and wakes it again with Wake-on-LAN.
4. Shuts the host down pre-emptively when a thunderstorm approaches, and brings it back after the all-clear.
5. Logs every action and sends notifications through a service outside the homelab.

The goals are lower electricity use and protection against storm-related power problems. The owner will later expose the UI to the internet through Pangolin, so **security is priority 1** in every design decision.

### Related repository

The homelab itself lives in the **LabyrinthStack** repo (GitOps: OpenTofu + Ansible + Forgejo Actions, SOPS/age secrets). labpower is deployed *from* that repo by an Ansible role. Work in this repo is the Go application; the LabyrinthStack changes are listed separately at the end of this file.

## Working rules for the agent

- Work **milestone by milestone** (see "Milestones"). Finish and test one before starting the next. Each milestone has acceptance criteria; meet them all.
- **Dry-run is the default.** Every code path that changes real state (start/stop guest, host shutdown, WoL) must respect `dry_run: true` and only log what it would do. Never flip the default.
- **Never call a real Proxmox API, router, or weather service in tests.** Use the interfaces and fakes described below.
- **Never commit secrets.** Tokens, passwords, and keys are read from files or systemd credentials at runtime. Example configs use obvious placeholders.
- Keep dependencies to the list in "Tech stack". Adding a new dependency requires a note in the PR/commit explaining why the standard library is not enough.
- Security requirements in this file are **requirements, not suggestions**. If one seems impossible or conflicts with another, stop and ask instead of weakening it.
- Values marked `TODO(owner)` are unknown and must be asked for or left as placeholders — never guess them.
- Before finishing any task run: `gofmt -l .` (no output), `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, `govulncheck ./...`.

## Architecture

```
                         INTERNET
  ┌───────────────┐      ┌──────────────────────────────┐      ┌────────────────────┐
  │ Phone/laptop  │─────▶│ vps-bastion                  │      │ Weather sources    │
  │ Pangolin app  │      │ Pangolin (Traefik, Gerbil)   │      │ lightning strikes, │
  └───────────────┘      │ SSO + TOTP, CrowdSec         │      │ warnings, forecast │
                         └──────────────┬───────────────┘      └─────────▲──────────┘
                                        │ WireGuard tunnel               │ HTTPS poll
                                        │ (initiated outbound by Newt)   │ (outbound)
  ══ DMZ · VLAN 70 ═════════════════════▼════════════════════════════════╪══════════
   ┌──────────────────────┐   ┌──────────────────────────────────────────┴───────┐
   │ Sensors (I2C)        │──▶│ Raspberry Pi Zero 2 W                            │
   │ AS3935 lightning     │   │  Newt ──▶ labpower @ 127.0.0.1:8080 ──▶ SQLite   │
   │ DS3231 RTC           │   └──────────────────────────┬───────────────────────┘
   └──────────────────────┘                              │ TCP 8006 + WoL
  ═══════════════════════════════════════════════════════▼══════════════════════════
                                   ┌──────────────────────────────┐
                                   │ Router / firewall            │
                                   │ pinhole Pi → Proxmox :8006   │
                                   │ WoL relay into VLAN 10       │
                                   └──────────────┬───────────────┘
  ══ MANAGEMENT · VLAN 10 ════════════════════════▼═════════════════════════════════
   ┌───────────────────────────────┐        ┌────────────────────────────────────┐
   │ Proxmox host                  │───────▶│ Guests                             │
   │ API :8006, scoped API token   │        │ scheduled + always-on (protected)  │
   └───────────────────────────────┘        └────────────────────────────────────┘
```

Key properties the implementation must preserve:

- **All connections start at the Pi and go outward.** labpower binds to `127.0.0.1` only. Newt (Pangolin's tunnel client) runs on the same Pi and forwards to it. Nothing can reach labpower except through Pangolin.
- **labpower must not depend on anything running on the Proxmox host**, because it has to work while the host is off (to wake it). This includes notifications: use a service outside the homelab.
- **Wake-on-LAN crosses VLANs via the router.** The Pi is in VLAN 70; the Proxmox NIC is in VLAN 10. Support these WoL methods behind one interface, selected in config: `router_api` (router's own WoL service), `unicast` (UDP to a static-ARP IP in VLAN 10), `broadcast` (directed broadcast). `TODO(owner)`: which router/firewall is used.
- **The Proxmox API token is narrowly scoped**: `VM.Audit` + `VM.PowerMgmt` only on scheduled guests' paths (e.g. `/vms/201`), `Sys.Audit` + `Sys.PowerMgmt` on the node. Always-on guests (`lxc-forge`, `lxc-edge`) are not in the token's scope at all, so even a compromised Pi cannot stop them.

### Homelab facts relevant to this project

- Proxmox host on management VLAN 10 (`10.22.10.0/24`). `TODO(owner)`: host IP, node name, NIC MAC address.
- DMZ VLAN 70 (`10.22.70.0/24`). `TODO(owner)`: Pi IP.
- Always-on guests: `lxc-forge` (Forgejo, CI runner, OpenTofu state backend) and `lxc-edge` (internet-facing ingress). Host shutdown takes these down too; the UI must warn clearly.
- Scheduled guest examples: `vm-media` (mounts the ZFS media shares; a main reason the HDDs can't spin down). `TODO(owner)`: the full list of guests to schedule.
- Recurring jobs that need the host on: weekly Trivy scan (Mon 04:00 UTC) and Renovate. Config validation should warn if the host schedule excludes a configured "required window".
- No UPS currently. The server BIOS is set to "Restore on AC power loss: Stay off" so labpower decides when it boots after an outage.

## Tech stack

- **Go 1.25+** (needed for `http.CrossOriginProtection`).
- Build: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` (64-bit Raspberry Pi OS Lite). Never build on the Pi.
- Allowed dependencies:
  - `gopkg.in/yaml.v3` — config
  - `modernc.org/sqlite` — pure-Go SQLite (keeps CGO off)
  - `github.com/go-webauthn/webauthn` — passkeys
  - `github.com/pquerna/otp` — TOTP backup
  - `golang.org/x/crypto/argon2` — password hashing
  - `golang.org/x/time/rate` — rate limiting
  - `periph.io/x/...` — I2C/GPIO for the AS3935 sensor
  - htmx, vendored into `web/static/` (no CDN)
- Everything else from the standard library: `net/http` (Go 1.22+ routing patterns), `html/template`, `embed`, `log/slog`, `time/tzdata` (import it so timezones work on any OS image).
- Proxmox client and WoL are hand-written (small; see below). Do not add a Proxmox SDK.
- The DS3231 RTC is handled by the kernel (`dtoverlay=i2c-rtc,ds3231`), not by Go code.

## Repository layout

```
labpower/
  cmd/labpower/main.go        # flags, wiring, subcommands (serve, status, wake, guest, check-config)
  internal/config/            # YAML load + validation
  internal/schedule/          # PURE functions: window evaluation, boundaries (no I/O)
  internal/clock/             # Clock interface + real and fake implementations
  internal/proxmox/           # thin API client + interface + fake
  internal/wol/               # magic packet; router_api / unicast / broadcast senders
  internal/weather/           # providers, threat-level monitor, haversine
  internal/sensor/as3935/     # local lightning sensor driver (periph.io)
  internal/notify/            # notifier interface (ntfy, Pushover, …) + fake
  internal/store/             # SQLite: migrations, overrides, events, state, auth
  internal/engine/            # scheduler loop, priorities, host state machine
  internal/auth/              # passkeys, TOTP, sessions, rate limiting, step-up
  internal/web/               # handlers, middleware (headers, CSRF), templates
  web/templates/              # html/template files (embedded)
  web/static/                 # css, htmx.min.js, self-hosted fonts (embedded)
  deploy/labpower.service     # hardened systemd unit
  deploy/config.example.yaml
  Makefile                    # build, test, lint, vuln, release
```

`schedule` must stay free of I/O. `engine` depends only on interfaces (`proxmox.Client`, `wol.Sender`, `weather.Monitor`, `notify.Notifier`, `clock.Clock`, `store.Store`) so it can simulate a week in milliseconds in tests.

## Configuration

Schedules live in Git (deployed by Ansible). Runtime state (overrides, sessions, events) lives in SQLite. Guests are referenced by name; resolve VMIDs via `GET /cluster/resources` at startup and on refresh.

```yaml
dry_run: true
timezone: Europe/Amsterdam            # TODO(owner): confirm
listen: 127.0.0.1:8080
public_url: https://power.example.com # TODO(owner): Pangolin hostname
trusted_proxy: 127.0.0.1              # only trust X-Forwarded-For from Newt

proxmox:
  url: https://10.22.10.X:8006        # TODO(owner)
  node: pve01                         # TODO(owner)
  token_id: labpower@pve!pi
  token_secret_file: ${CREDENTIALS_DIRECTORY}/proxmox-token
  tls_fingerprint: "AB:CD:…"          # pin the self-signed cert; never skip verification

wol:
  method: unicast                     # router_api | unicast | broadcast — TODO(owner)
  mac: "aa:bb:cc:dd:ee:ff"            # TODO(owner)
  target: 10.22.10.250                # unicast: static-ARP IP; broadcast: 10.22.10.255
  retries: 3
  wake_timeout: 5m

schedules:
  daytime:
    - { days: mon-fri, on: "07:00", off: "01:00" }
    - { days: sat-sun, on: "09:00", off: "01:30" }
  evenings:
    - { days: mon-sun, on: "17:00", off: "00:30" }

required_windows:                     # validation warns if host is off during these
  - { name: trivy, days: mon, from: "05:30", to: "07:00" }   # 04:00 UTC in CEST — TODO(owner)

host:
  schedule: daytime
  shutdown_grace: 10m

guests:
  lxc-forge: { always_on: true }
  lxc-edge:  { always_on: true }
  vm-media:  { schedule: evenings, depends_on: [] }

weather:
  location: { lat: 0.00, lon: 0.00 }  # TODO(owner): 2 decimals is enough
  lightning_network: { enabled: true }
  local_sensor: { enabled: true, bus: /dev/i2c-1, address: 0x03, irq_gpio: 17 }
  warnings: { provider: meteoalarm, region: "TODO" }
  forecast: { provider: open-meteo, interval: 30m }
  levels:
    warning: { strike_radius_km: 30, countdown: 10m }
    danger:  { strike_radius_km: 12 }
    all_clear_after: 30m
  on_stale_data: shutdown_if_elevated
  stale_after: 10m

notify:
  provider: ntfy                      # must be outside the homelab
  url: https://ntfy.sh/TODO
  token_file: ${CREDENTIALS_DIRECTORY}/notify-token

auth:
  rp_id: power.example.com            # WebAuthn relying party
  session_idle: 30m
  session_absolute: 12h
```

Validation must reject: unknown schedule names, unknown guests, dependency cycles, guest windows outside the host window, `always_on` combined with a schedule, missing secrets files, and `dry_run` absent (it must be explicit).

## Scheduling engine

### Priority order

**Safety (weather, later UPS) > manual override > schedule.**

A weather lockout beats a manual "keep on". It can only be bypassed with an explicit "ignore weather for N minutes" action that requires step-up authentication and is logged and notified.

### Edge-triggered with catch-up

The engine ticks every 30 seconds:

1. Load `last_tick` from the `state` table.
2. Compute every schedule boundary crossed in `(last_tick, now]` for the host and each guest.
3. For each crossed boundary, apply the new desired state unless an active override or safety lockout says otherwise.
4. Expire overrides whose `until` has passed; apply the schedule's state at that moment.
5. Evaluate the weather threat level and apply safety actions.
6. Save `now` as `last_tick`.

Consequences that must hold (write tests for each):

- A guest started manually between boundaries stays on until the next boundary (starts made in the Proxmox UI are treated like overrides).
- After a Pi reboot, missed boundaries are applied once (most recent state wins), not replayed one by one.
- The engine never acts before the clock is trustworthy (NTP synced or RTC present). Expose this as `clock.Trusted()`.

### Guests

- Start in `depends_on` order; shut down in reverse order.
- Use `shutdown` with a timeout, never `stop`. Wait for the task (UPID) to finish before continuing.
- `always_on` guests are never started or stopped by labpower.

### Host state machine

```
Off ──(WoL sent)──▶ Waking ──(API responds)──▶ On
 ▲                    │                         │
 │         (timeout: resend WoL up to           │ host window ends / vacation /
 │          `retries`, then alert + Failed)     │ weather Danger, and no active tasks
 │                                              ▼
 └─────────────(API unreachable)──────── ShuttingDown
```

- Before `ShuttingDown`: check `GET /nodes/{node}/tasks?source=active`. If a backup or other task runs, postpone (re-check each tick) and notify — except at weather level Danger, where it shuts down after a short grace period regardless.
- Host shutdown shuts down scheduled guests first (reverse dependency order), then calls the node shutdown.
- The host is only woken when at least one guest or the host schedule wants it on, the weather is not elevated, and the clock is trusted.

### Proxmox API surface

Auth header: `Authorization: PVEAPIToken=<token_id>=<secret>`. TLS verified against the pinned fingerprint.

| Purpose | Endpoint |
|---|---|
| List guests, names, status | `GET /cluster/resources?type=vm` |
| Host reachable | `GET /nodes/{node}/status` |
| Start / shut down guest | `POST /nodes/{node}/{qemu\|lxc}/{vmid}/status/{start\|shutdown}` |
| Task status | `GET /nodes/{node}/tasks/{upid}/status` |
| Active tasks | `GET /nodes/{node}/tasks?source=active` |
| Host shutdown | `POST /nodes/{node}/status` with `command=shutdown` |

## Weather safeguard

Providers implement one interface; a `Monitor` combines them into a threat level.

| Level | Trigger | Action |
|---|---|---|
| Normal | — | Schedule as usual |
| Watch | Thunderstorm forecast in the next hours (Open-Meteo weather codes 95/96/99, CAPE) | Notify only |
| Warning | Official orange/red thunderstorm warning, or strikes within `warning.strike_radius_km` | Notify with countdown; host shuts down after `countdown` unless cancelled (cancel = step-up) |
| Danger | Strikes within `danger.strike_radius_km`, or confirmed local sensor detection | Immediate clean shutdown |
| All clear | No strikes within warning radius for `all_clear_after` and no active warning | Return to schedule |

- Sources: a real-time lightning network (e.g. Blitzortung — **check its terms of use and access method before implementing**; private non-commercial use), MeteoAlarm or KNMI warnings, Open-Meteo forecast, and the local AS3935 sensor.
- Filter lightning data by a bounding box around home **before** heavier processing (memory is limited).
- Distances with the haversine formula.
- Stale data: if data is older than `stale_after` while the level is Warning or higher, treat as Danger. If stale during Normal/Watch, keep running and notify.
- The AS3935 is noisy; a single local detection only counts toward Danger if another source agrees, or if two local detections occur within 5 minutes. Make this configurable.
- Ship with weather in **notify-only mode** first (config `weather.mode: notify|enforce`, default `notify`).

## Data model (SQLite)

Use WAL mode, a single connection pool, and embedded numbered migrations. Keep writes minimal (SD card).

```sql
CREATE TABLE overrides (
  id          INTEGER PRIMARY KEY,
  target      TEXT NOT NULL,              -- guest name or 'host'
  action      TEXT NOT NULL CHECK (action IN ('on','off','pause','ignore_weather')),
  until       INTEGER,                    -- unix seconds; NULL = until next boundary/manual
  created_by  TEXT NOT NULL,              -- user id or 'system'
  created_at  INTEGER NOT NULL,
  cancelled_at INTEGER
);
CREATE TABLE events (
  id      INTEGER PRIMARY KEY,
  at      INTEGER NOT NULL,
  kind    TEXT NOT NULL,                  -- guest_start, host_shutdown, login_ok, login_fail, weather_level, ...
  target  TEXT,
  actor   TEXT NOT NULL,                  -- 'schedule' | 'weather' | 'user:<id>' | 'system'
  reason  TEXT,
  ip      TEXT,
  dry_run INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL, password_hash TEXT, totp_secret_enc BLOB);
CREATE TABLE credentials (id BLOB PRIMARY KEY, user_id TEXT NOT NULL, data BLOB NOT NULL, created_at INTEGER NOT NULL, last_used INTEGER);
CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, user_id TEXT NOT NULL, created_at INTEGER NOT NULL,
                       last_seen INTEGER NOT NULL, stepup_until INTEGER, ip TEXT, user_agent TEXT);
```

Prune `events` older than 90 days daily.

## Security requirements

These apply from milestone 1, even while the UI is VPN/Pangolin-private.

### Authentication

- Primary login: **passkeys (WebAuthn)**. Backup: TOTP. No password-only login. If a password exists, hash with Argon2id at moderate memory (≈19–46 MiB) and allow at most 2 concurrent hash operations (Pi has 512 MB RAM).
- First-run enrolment: a one-time enrolment token printed by `labpower enrol` on the Pi's console, valid 15 minutes. No default credentials, ever.
- Sessions: 32 random bytes, stored **hashed**; cookie `__Host-labpower` with `HttpOnly; Secure; SameSite=Strict; Path=/`. Idle timeout 30 min, absolute 12 h. New token on login.
- **Step-up**: host shutdown, vacation mode, ignore-weather, and security settings need a fresh passkey assertion (valid 5 min, stored as `stepup_until`).
- Rate limiting: per IP and per account on login and step-up, with exponential backoff and lockout; log and notify on bursts. Trust `X-Forwarded-For` only when the peer is `trusted_proxy`.

### HTTP hardening

- `http.CrossOriginProtection` on all state-changing routes, **plus** a per-session CSRF token sent by htmx as a header (`hx-headers`), verified server-side.
- Only POST changes state. GET is always side-effect free.
- Headers on every response: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Permissions-Policy: interest-cohort=()`, `Cache-Control: no-store` on authenticated pages. HSTS is set by Pangolin.
- No inline scripts or styles; set `htmx.config.allowEval = false`, `htmx.config.selfRequestsOnly = true`.
- `http.Server` timeouts: `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 30s` (longer only on the SSE route), `IdleTimeout 60s`, `MaxHeaderBytes 16 KiB`. Limit request bodies with `http.MaxBytesReader`.
- `html/template` only; never wrap user-influenced data in `template.HTML`.
- Errors shown to users are generic; details go to the log.

### Audit and alerts

- Every login attempt (success and failure), every action, every override, and every weather level change is written to `events` with actor and IP.
- Notify on: login from a new IP, failed-login bursts, every host shutdown and wake (including failures), weather level ≥ Warning, and any ignore-weather action.

### Deployment hardening (systemd unit in `deploy/`)

```
[Service]
DynamicUser=yes
StateDirectory=labpower
LoadCredential=proxmox-token:/etc/labpower/proxmox-token
LoadCredential=notify-token:/etc/labpower/notify-token
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=no            # needs /dev/i2c-1 and GPIO; grant via DeviceAllow
DeviceAllow=/dev/i2c-1 rw
DeviceAllow=/dev/gpiochip0 rw
SupplementaryGroups=i2c gpio
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallArchitectures=native
MemoryDenyWriteExecute=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictNamespaces=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
MemoryMax=150M
```

### Supply chain

`govulncheck` in CI, Renovate for Go modules, Trivy filesystem scan of the release binary. Pin htmx and fonts by checksum in the repo.

## UI specification

Server-rendered `html/template` + htmx, hand-written CSS, **dark theme only**, mobile-first. Everything is embedded in the binary; no external requests (fonts are self-hosted woff2, SIL OFL licensed).

### Design tokens (`web/static/app.css` `:root`)

| Token | Value | Use |
|---|---|---|
| `--bg` | `#0f1115` | Page |
| `--surface` | `#171a21` | Cards |
| `--surface-2` | `#1b1f28` | Stat tiles, timeline tracks |
| `--button` | `#1f2430` | Secondary buttons |
| `--nav` | `#13161c` | Sidebar / bottom tab bar |
| `--border` | `#262b36` | Card borders |
| `--border-strong` | `#2f3542` | Button borders |
| `--text` | `#e6e8ec` | Primary text |
| `--text-2` | `#c9d1dc` | Emphasised secondary text |
| `--muted` | `#9aa3b2` | Secondary text (≥ 4.5:1 on surface) |
| `--accent` | `#4da3ff` | Primary buttons (text `#0b1320`), links, active nav |
| `--ok` / `--ok-text` | `#3fb950` / `#56d364` | Running |
| `--warn` / `--warn-text` / `--warn-border` | `#d29922` / `#e3b341` / `#5c4516` | Overrides, weather Watch/Warning |
| `--danger` / `--danger-text` / `--danger-border` | `#b62324` / `#ff7b72` / `#6e2b2b` | Destructive actions, errors |
| `--stopped` | `#a3abba` | Stopped (hollow dot) |

Fonts: **Space Grotesk** (headings, wordmark), **IBM Plex Sans** (body), **IBM Plex Mono** (guest names, times, numbers). Fallback to system stacks.

Rules: status is always colour **and** text ("Running", "Stopped"); touch targets ≥ 44 px; card radius 14 px, button radius 10 px; real `<button>`, `<a>`, `<label>`+`<input>` elements; `aria-label` on icon-only buttons; `aria-current="page"` on the active nav item. Icons are inline stroke SVGs (power, bolt, lock, key, check, alert, chevron, user).

### Screens

**Login** — wordmark, "Sign in with passkey" primary button, "Use authenticator code" secondary link. Generic error messages only.

**Dashboard (phone, 390 px wide)** — top to bottom:
1. Header: `labpower` wordmark, date/time, account button (→ Security).
2. Host card: "PROXMOX HOST" label + status pill; large line "Shuts down at 01:30"; muted "In 11h 20m · schedule `daytime`"; buttons "Keep on tonight" (secondary) and "Shut down…" (danger outline → confirmation screen).
3. Weather card (border tinted by level): bolt icon, level pill (Watch/Warning/Danger/Normal), headline ("Thunderstorms possible 18:00–22:00"), three stat tiles (nearest strike km, active warnings, local sensor), footnote about the auto-shutdown threshold. At Warning show the countdown and a "Cancel shutdown" (step-up) button.
4. "Guests" heading with "View timeline" link.
5. Guest cards: mono name + LXC/VM badge + status. Scheduled guests show "Schedule `name` · 17:00–00:30" and actions ("Start for 2h", "Start until…", or "Stop", "Extend…"). An active override shows an amber strip ("Manual start · on until 17:00, then schedule") with a Cancel button. Always-on guests show a lock icon, "Always on · purpose", and **no power buttons**.
6. Bottom tab bar: Dashboard, Timeline, Vacation, Events.

**Timeline (phone)** — segmented buttons Today / Tomorrow / Week; one card with an hour axis (00 06 12 18 24) and one 22 px track per row (host, each guest, and a "Storm risk (forecast)" row). Segment colours: scheduled on = accent at 55 % alpha, override = `--warn`, always-on = ok at 40 % alpha, storm risk = danger at 40 % alpha. A 2 px "now" line on every track. Legend below. Second card "Coming up": time (mono) + description per upcoming event (e.g. "17:00 vm-media override ends, schedule takes over", "01:30 Host shuts down · wakes Mon 07:00 via Wake-on-LAN"). Render tracks server-side (percent widths or SVG); no JS required.

**Shut down host (phone)** — back link; H1 "Shut down the host?"; explanatory line; card "What goes down" with a danger callout when always-on guests are affected (names + what they run) and a list of guests with current state; card "Pre-checks" (no active tasks, no ZFS scrub, last successful WoL date) — if a check fails, show it and disable confirm; card "Wake up again" with radio options: at next schedule (shows time), on a date (vacation mode), only manually; primary danger button "Confirm with passkey" (triggers step-up) and a Cancel link.

**Vacation** — return date/time picker, what will be shut down, weather still enforced, confirm with passkey. Active vacation shows a banner on every page with "End vacation now".

**Events** — reverse-chronological list: time, description, actor, dry-run marker; filter by kind; paginated.

**Security** — registered passkeys (add/remove, step-up), TOTP status, active sessions with "Revoke", recent login attempts.

**Desktop (≥ 1024 px)** — 232 px left sidebar (wordmark, nav: Dashboard, Timeline, Vacation, Weather, Events, Security; session box at the bottom "Signed in with passkey · via Pangolin · session ends in N min"). Main area: page header with "Pause all schedules" and "Vacation mode…" buttons; three cards in a row (Host, Weather, Coming up); a guest table (Guest, Status, Schedule, Override, Actions — always-on rows show "Protected"); "Recent events" card with the last few events.

**Live updates** — htmx polling of status fragments every 15 s, or SSE on `/events/stream`. Both must work under the CSP above.

### HTTP routes

```
GET  /login                      GET  /                     (dashboard)
POST /login/passkey/begin        GET  /timeline?day=
POST /login/passkey/finish       GET  /vacation
POST /login/totp                 GET  /events
POST /logout                     GET  /security
POST /stepup/begin|finish        GET  /partials/{host,weather,guests}   (htmx fragments)
POST /guests/{name}/start        body: until=<rfc3339>|duration=<2h>|next_boundary
POST /guests/{name}/stop
POST /overrides/{id}/cancel
POST /host/shutdown              (step-up) body: wake=schedule|date|manual, date=
POST /host/wake
POST /vacation                   (step-up)   POST /vacation/end
POST /weather/ignore             (step-up) body: minutes=
POST /schedules/pause            body: target=, until=
GET  /healthz                    (no auth, returns only "ok")
```

## CLI

`labpower serve`, `labpower check-config`, `labpower status`, `labpower wake`, `labpower guest start|stop <name>`, `labpower host shutdown`, `labpower enrol`. CLI actions respect `dry_run` and are logged as `actor=cli`.

## Testing

- `internal/schedule`: table-driven tests for windows crossing midnight, day-of-week boundaries, both DST transitions (nonexistent 02:30 in spring, duplicated 02:30 in autumn), overlapping windows, and empty schedules.
- `internal/engine`: fake clock + fake Proxmox + fake WoL + fake weather; simulate full weeks. Cover catch-up after downtime, manual starts between boundaries, override expiry, dependency ordering, task-postponed shutdown, WoL retry and failure, weather lockout beating overrides, stale weather data.
- `internal/web` + `internal/auth`: `httptest` tests for CSRF rejection, cross-origin rejection, session expiry, session revocation, step-up enforcement on every protected route, rate limiting, security headers on every response, and that `/healthz` leaks nothing.
- Fuzz tests for config parsing and schedule parsing.

## Milestones

1. **Plumbing** — config + validation, Proxmox client, WoL senders, CLI (`status`, `wake`, `guest start/stop`), dry-run. *Accept:* `labpower status` lists guests against a fake server; all WoL methods produce correct packets in tests.
2. **Guest scheduling (dry-run)** — schedule package, engine, store, event log. *Accept:* a simulated week produces exactly the expected actions; runs a week on the Pi in dry-run.
3. **Guest scheduling (live)** — dry-run off for guests only.
4. **Host power management** — state machine, task check, WoL retries, notifications. *Accept:* engine tests for every transition; a real shutdown/wake cycle succeeds.
5. **Weather (notify-only)** — providers, monitor, AS3935 driver. *Accept:* runs through real storms; alerts match reality.
6. **Web UI + auth** — all screens above, passkeys, TOTP, sessions, step-up, CSRF, headers, rate limiting. *Accept:* all security tests pass; OWASP ZAP baseline scan shows no medium/high findings.
7. **Weather enforce mode** — auto-shutdown and all-clear recovery.
8. **Release + deploy** — Makefile release target, Forgejo workflow, systemd unit. *Accept:* `systemd-analyze security labpower` exposure score ≤ 2.0.
9. **Later** — UPS via NUT (`shutdown_after_on_battery`), Prometheus `/metrics`, host on-hours/energy estimate.

Public exposure through Pangolin happens only after milestone 8 and a manual security review.

## LabyrinthStack changes (separate repo, separate PRs)

These are **not** done in this repo; list them in a PR description or issue for the LabyrinthStack repo.

- **OpenTofu (`tofu/prod/`)**: add Proxmox tag `nightoff`/schedule metadata where useful; `lifecycle { ignore_changes = [started] }` on scheduled guests; `on_boot = false` for scheduled guests, `true` for `lxc-forge` and `lxc-edge`; the `labpower@pve` user, role, token, and per-guest ACLs.
- **Ansible**: add the Pi to `ansible/inventory/hosts`; new role `ansible/roles/labpower/` that installs the binary, `config.yaml`, credentials (from vault-encrypted `host_vars`), and the systemd unit; Pi hardening (SSH keys only, unattended-upgrades, log2ram or read-only overlay, nftables rules below, Newt as a native systemd service with its own Pangolin site); make `site.yml` plays tolerate scheduled hosts being powered off.
- **Proxmox host**: persist Wake-on-LAN (`post-up /usr/sbin/ethtool -s <nic> wol g`); BIOS WoL on, ErP off, "Restore on AC power loss: Stay off".
- **Firewall (router + Pi nftables)**, Pi in VLAN 70:

  | Direction | Allow |
  |---|---|
  | Pi → Proxmox | TCP 8006 |
  | Pi → router | WoL method (router API port, or UDP 9 to the static-ARP IP) |
  | Pi → vps-bastion | Pangolin/Gerbil tunnel ports (`TODO(owner)`: confirm) |
  | Pi → internet | TCP 443 to weather and notification services |
  | Pi → router | DNS, NTP |
  | Admin → Pi | TCP 22 |
  | everything else | deny |

- **Pangolin**: dedicated site for the Pi's Newt; labpower as a **private** resource (Pangolin client access) first; role restricted to the owner with 2FA enforced; **no auth-bypass rules**; CrowdSec/Geoblock enabled if it becomes public.
- **Forgejo Actions**: build `labpower` for arm64 on tag, run tests/vet/staticcheck/govulncheck, publish the binary as a release asset with checksum; Renovate tracks Go modules.

## Hardware (for reference)

Raspberry Pi Zero 2 W (64-bit Pi OS Lite, read-only overlay), 5 V 2.5 A PSU, high-endurance microSD, micro-USB Ethernet adapter (WiFi disabled — it interferes with the AS3935), DS3231 RTC and AS3935 lightning sensor on I2C bus 1 (sensor mounted a few cm from the board), optional PiSugar battery HAT for power-loss detection.
