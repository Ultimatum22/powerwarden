# PLAN.md: where labpower stands and what's left

_Last reviewed: 2026-09-29 against `main` @ `fce9d69`. Updated the same day:
local dev loop added (`make dev`, see `deploy/README.md`)._

**Goal.** Manage the homelab from anywhere: start and stop Proxmox guests on a
schedule, shut the host down before thunderstorms and bring it back after, and
reach it all safely from away from home through Pangolin.

---

## 1. TL;DR

- **Code for milestones 1–8 is committed.** All quality gates are green:
  `gofmt`, `go vet`, `staticcheck`, `go test -race`, `govulncheck` (0 reachable
  vulns; see §3.4).
- **Nothing has been field-verified on the Pi yet.** Every acceptance criterion
  that needs real hardware is still open: the week-long dry-run, a real
  shutdown/wake cycle, real storms, the ZAP scan, and `systemd-analyze security`.
- **The web UI has blocking bugs.** Step-up actions (host shutdown, vacation,
  ignore-weather) can't be completed from the browser, and inline styles break
  under the CSP. These must be fixed before the UI is usable remotely.
- **Weather auto-stop is weak right now.** No lightning-network source is wired
  in (Blitzortung has no public API). Only MeteoAlarm warnings and the local
  AS3935 can raise the level, and enforcement is off by default
  (`weather.mode: notify`).
- **Several spec features are missing:** timeline tracks, passkey/TOTP/session
  management, live updates, events filtering, some config validation, and some
  alerts. See §3.

Recommended order: **fix blockers (Phase A) → fill spec gaps (B) → owner
decisions + lightning source (C) → staged rollout on the Pi (D) →
LabyrinthStack changes (E) → remote access (F)**.

---

## 2. Milestone status

| # | Milestone | Code | Field acceptance |
|---|---|---|---|
| 1 | Plumbing (config, Proxmox client, WoL, CLI) | ✅ | ✅ tests only (fake server, packet tests) |
| 2 | Guest scheduling (dry-run) | ✅ | ⏳ "runs a week on the Pi in dry-run": **not done** |
| 3 | Guest scheduling (live) | ✅ | ⏳ not run against the real Proxmox |
| 4 | Host power management | ✅ | ⏳ "a real shutdown/wake cycle succeeds": **not done** (WoL method unknown) |
| 5 | Weather (notify-only) | ⚠️ partial: no lightning network | ⏳ "runs through real storms": **not done** |
| 6 | Web UI + auth | ⚠️ partial: see §3.1, §3.2 | ⏳ ZAP baseline: **not done**; WebAuthn ceremony never tested end to end |
| 7 | Weather enforce mode | ✅ (engine) | ⏳ depends on 5 |
| 8 | Release + deploy | ✅ | ⏳ `systemd-analyze security` ≤ 2.0: **not verified** |
| 9 | Later (UPS/NUT, `/metrics`, energy estimate) | ❌ | — |

Extra work beyond the spec: `fce9d69` added a Dockerfile, docker-compose, and
GitHub Actions CI/release alongside the Forgejo workflows (see decision D8).

---

## 3. Gaps found in review

### 3.1 Blockers (the UI can't be used safely or fully without these)

1. **Step-up has no client side.** `/stepup/begin|finish` exist server-side,
   but no JS calls them. `web/templates/host_shutdown.html`, `vacation.html` and
   the dashboard's ignore-weather form just `hx-post`, and `requireStepUp`
   (`internal/web/middleware.go:105`) returns 403. **Result: host shutdown,
   vacation, ignore-weather, and "End vacation" all fail from the UI.**
   → Add `web/static/stepup.js`: intercept forms marked `data-stepup`, run the
   WebAuthn assertion via `/stepup/begin|finish`, then submit. Show a generic
   error on failure. Add an httptest for the full flow (403 → step-up → 200).
2. **Inline `style="…"` attributes violate the CSP** (`style-src 'self'`). They
   appear in `dashboard.html`, `vacation.html`, `events.html`, `login.html`,
   and `enrol.html`. Browsers drop them, so layout breaks, and
   **`login.html`'s `#totp-form` `display:none` is ignored**, leaving the TOTP
   form always visible.
   → Move them to classes in `app.css`. Add a test that fails when any template
   contains `style=`, `<script>` without `src`, or `on*=` attributes.
3. **`labpower host shutdown` (CLI) doesn't wait for guest shutdowns.**
   `cmd/labpower/host.go:87` discards the UPID from `ShutdownGuest` and
   calls the node shutdown immediately. CLAUDE.md says to wait for each task.
   The engine does wait (`internal/engine/guests.go:205`); the CLI doesn't.
   This showed up in `make dev`, where fakepve's tasks take 3 s.
   → Wait for each task (with a timeout) in reverse dependency order, and
   reuse the engine's wait logic.
4. **The CLI help text is stale.** `cmd/labpower/main.go:50` still says
   "web UI not yet implemented". Trivial, but it misleads the operator.

### 3.2 Spec features missing or incomplete

| Area | Gap | Where |
|---|---|---|
| Timeline | ✅ Phase B (storm-risk row still open) | `internal/web/timeline.go` |
| Security page | Read-only. Missing: add/remove passkey (step-up), TOTP enrol/disable, **revoke session**, recent login attempts. No routes exist. | `web/templates/security.html`, `internal/web/routes.go` |
| Live updates | No `hx-trigger="every 15s"` polling and no SSE. The dashboard is static until reload. | `dashboard.html`, `partial_*.html` |
| Events | Hard-coded last 100. No filter by kind, no pagination, no dry-run marker check. | `internal/web/handlers_pages.go:122` |
| Weather page | The desktop nav in the spec includes "Weather"; there's no page. | `layout.html` |
| Desktop dashboard | Needs checking against the spec: "Pause all schedules" / "Vacation mode…" header buttons, 3-card row, guest table with "Protected", "Recent events" card, session box. | `dashboard.html`, `layout.html` |
| Host shutdown pre-checks | Only active tasks. Missing: ZFS scrub check, last successful WoL date, **disable confirm when a check fails**. | `host_shutdown.html` |
| Alerts | No notification for **login from a new IP** or **failed-login bursts**. | `internal/web/handlers_auth.go`, `internal/auth/ratelimit.go` |
| Config validation | `required_windows` are parsed but **never checked** against the host schedule. **Guest windows outside the host window** are not rejected. | `internal/config/config.go:405` |
| Clock trust | `clock.Real.Trusted()` only checks `year >= 2026`. After a power cut, Pi OS `fake-hwclock` restores the last-saved time, which passes this check even though it's wrong, so catch-up could apply wrong boundaries. → Use the kernel NTP sync status (`syscall.Adjtimex`, `STA_UNSYNC` clear), or require the RTC (`/dev/rtc0` present and `/sys/class/rtc/rtc0` set by the `ds3231` overlay). | `internal/clock/clock.go` |
| Fonts | Not vendored. CSS uses fallbacks. Add Space Grotesk / IBM Plex woff2 (OFL) and their checksums. | `web/static/`, `CHECKSUMS.txt` |
| WebAuthn E2E | The ceremony isn't tested end to end. Consider a small software authenticator in tests (ES256, `none` attestation) so login and step-up are covered. | `internal/auth`, `internal/web` |

### 3.3 Weather: what can actually trigger a shutdown today

| Level | Sources wired in now | Consequence |
|---|---|---|
| Watch | Open-Meteo codes 95/96/99 + CAPE | ✅ notify |
| Warning | MeteoAlarm orange/red only (**strike radius unusable: no network source**) | Countdown shutdown in `enforce` mode |
| Danger | 2× AS3935 within 5 min, or stale data at Warning+ | Immediate shutdown in `enforce` mode |

**The AS3935 is the only real-time lightning input, and it's a single noisy
sensor.** To make storm auto-stop dependable, add a network lightning source
behind the existing `weather.LightningSource` interface (decision D6).

### 3.4 Housekeeping

- `govulncheck` reports GO-2026-5932 (`x/crypto/openpgp` unmaintained). It isn't
  called and has no fix, so it's informational only. Keep watching it.
- Naming: the repo/module is `powerwarden` (`github.com/Ultimatum22/powerwarden`)
  while the binary and docs say `labpower`. Pick one (decision D9).

---

## 4. Decisions needed from the owner (`TODO(owner)`)

These values must not be guessed. Most of them block Phase D.

| ID | Question | Blocks |
|---|---|---|
| D1 | Proxmox host IP, node name, NIC MAC | D, E |
| D2 | Pi IP in VLAN 70 | E |
| D3 | Router/firewall model → WoL method (`router_api` / `unicast` / `broadcast`) and, if `router_api`, its API | Host wake (M4 acceptance) |
| D4 | Full list of guests to schedule, their schedules, `depends_on` | D |
| D5 | Timezone confirm; Trivy/Renovate required windows in local time | Config |
| D6 | **Lightning network source.** Blitzortung is out (no public API). Candidates to research for terms and access: national met-office open data (e.g. KNMI), commercial APIs with a free private tier. Or accept "MeteoAlarm + AS3935 only". | Reliable weather enforce |
| D7 | Location (lat/lon, 2 decimals), MeteoAlarm region code, ntfy topic URL | Weather, notify |
| D8 | Deployment shape: **systemd unit (per spec)** or Docker? GitHub Actions, Forgejo, or both? The spec and LabyrinthStack assume systemd + Forgejo. Recommendation: systemd on the Pi, Forgejo CI, keep GitHub CI only if the repo is mirrored there. | Phase D/E |
| D9 | Name: `labpower` or `powerwarden`? | Cosmetic, do before first release |
| D10 | Pangolin hostname (`public_url`, `auth.rp_id`). **The RP ID can't change later without re-enrolling passkeys.** | Enrolment |

---

## 5. The plan

### Phase A: unblock the UI ✅ done (branch `phase-a`)

1. `stepup.js` + `data-stepup` forms, applied to host shutdown, vacation start
   and end, ignore-weather, and future security-settings actions.
2. Remove every inline style and move it to `app.css` classes.
3. Template lint test: no `style=`, no inline `<script>`, no `on*=` handlers.
4. Fix the `serve` help text.
5. Make the CLI host shutdown wait for guest tasks (§3.1 item 3).
6. httptest: the full step-up round trip for each protected route.

Status: items 1–6 done. Step-up is covered end to end by a software
passkey in `internal/web/softauthn_test.go` (real assertion, all four
protected routes, expiry, wrong key/origin, replay). Still to confirm by
hand in a real browser, since no authenticator runs in CI.

**Done when:** from a real browser via `LIVE=1 make dev` you can enrol, log in, start and stop a guest, and shut the host down with step-up,
with zero CSP violations in the console.

### Phase B: finish the spec ✅ done (branch `phase-a`)

All eleven items are done:

- **Security page:** add/remove passkeys, TOTP setup/disable, session
  revoke, recent sign-ins.
- **Alerts:** new-IP login, failed-login bursts, ignore-weather.
- **Clock trust:** NTP marker or RTC `hctosys`; `clock.trust: system` for
  dev.
- **Config validation:** guest windows outside the host window are
  rejected; `required_windows` produce warnings.
- **Live updates:** host, weather and guest cards poll every 15 s.
- **Timeline:** SVG tracks for Today/Tomorrow/Week, now-line, legend,
  "Coming up".
- **Shutdown pre-checks:** ZFS scrub and last successful WoL; confirm is
  disabled when a check fails.
- **Events page:** kind filter and pagination.
- **Weather page**, plus the desktop dashboard layout.
- **Fonts:** vendored, checksum-pinned, and verified by a test.
- **Tests:** a software passkey drives real WebAuthn registration and
  assertion.

Bugs found and fixed along the way:

- "Wake at next schedule" never woke the host (an off override with no
  end).
- An ignore-weather override shadowed a vacation or manual shutdown and
  woke the host.
- The vacation banner showed for manual shutdowns but not for real
  vacations.
- Enrolment didn't require discoverable passkeys, although login is
  usernameless.
- Times were shown in UTC. Ignore-weather was unbounded.
  `/schedules/pause` accepted any target.

Deliberately left out:

- **Storm-risk timeline row:** the forecast source only reports "thunder
  expected yes/no", not hours. Needs Open-Meteo's hourly weather codes
  kept in the engine.
- **SSE:** polling covers live updates.
- **Always-on guest "purpose" text:** not in the config.
- **Real-browser check:** screens were checked with headless Firefox at
  390 px and 1280 px. A real passkey ceremony still needs a manual test
  in `make dev`.

### Phase C: owner decisions + lightning source

**Done without owner input:**

- **Forecast storm windows:** Open-Meteo now reports 48 h of thunder
  hours. The timeline has a "Storm risk (forecast)" row, and the Watch
  headline reads "Thunderstorms possible 18:00–22:00". Watch itself still
  only uses the original lookahead.
- **Fetch cadence fix:** the forecast and warning providers were called on
  every 30 s tick (about 2,900 requests a day each). The configured
  `weather.forecast.interval` was never used. Now the forecast is fetched
  once per interval (default 30 min) and warnings every 5 min; a reused
  answer still counts as fresh for `stale_after`.

**Lightning source research (for D6).** Sources checked 2026-09-29:

| Source | Access | Fit | Blocker |
|---|---|---|---|
| Blitzortung | none for third parties | — | no public API (ruled out in M5) |
| KNMI "Lightning detection" (Météorage) | KNMI Data Platform | 5-min updates, NL/Europe | **not open data**: LVNL only; others must contract Météorage |
| EUMETSAT MTG Lightning Imager, LI-2-LFL flashes | Data Store API (free account) or EUMETCast | ~90 s timeliness, ~5 km mean location offset over Europe, rare false detections; detection efficiency still preliminary | NetCDF4/HDF5 in 10 s full-disc files: parsing needs a new Go dependency (CLAUDE.md requires a justification), and polling many files is heavy for a Pi Zero |
| Xweather Lightning API | free Developer tier, no card | JSON, strikes within a radius over the last 5 min, filtered server-side (tiny responses) | 15,000 calls/month (≈ one call every 3 min; poll faster only at Watch+). **Free-tier terms (non-commercial? attribution?) aren't on the public pages: read them at signup.** |
| Open-Meteo `lightning_potential` (ICON-D2) | free, already used | model forecast, not observed strikes | only useful to sharpen Watch, not Warning/Danger |

**Recommendation:** Xweather as the lightning network (it fits the
existing `LightningSource` interface, the memory budget and the polling
model), if its terms allow private 24/7 use. Adaptive polling keeps it
under the free quota: every 10 min at Normal, every 2 min from Watch.
MTG LI is the open fallback, but only if a NetCDF dependency is
acceptable.

**Still needs the owner:**

1. Answer D1–D10. For D6: sign up for Xweather, read its terms, and put
   the client ID/secret in a credentials file.
2. Fill in the real `config.yaml` in LabyrinthStack `host_vars`
   (vault-encrypted secrets). This repo keeps only the example.
3. If D3 is `router_api`: that vendor's client in
   `internal/wol/router_api.go`.

### Phase D: staged rollout on the Pi (field acceptance)

**Prepared:** `docs/ROLLOUT.md` is the step-by-step runbook (commands,
checks, acceptance and rollback per stage). The arm64 release build is
verified (`make release`). Preparing it found and fixed two deployment
bugs:

- The DynamicUser service couldn't read its root-owned 0600
  `/etc/labpower/config.yaml`. It now arrives via
  `LoadCredential=config.yaml` and `-config %d/config.yaml` (tested with a
  transient unit).
- The runbook runs `enrol` as the service's dynamic user, not root, so
  SQLite's WAL files stay writable.

Running the stages needs the Pi.

Each step runs for its full soak time before moving on. Rollback is always
"set `dry_run: true`, restart".

| Step | Config | Soak | Accept |
|---|---|---|---|
| D.1 | Deploy via systemd, `dry_run: true`, weather `notify` | 7 days | Events log shows exactly the expected guest/host actions per schedule (closes **M2**) |
| D.2 | `labpower wake` + manual `labpower host shutdown` (live, CLI) | 1 cycle | Host goes down cleanly, wakes via WoL (closes **M4**) |
| D.3 | `dry_run: false` for guests (host still manual) | 7 days | Guests start/stop on schedule; manual Proxmox-UI start survives until the next boundary (closes **M3**) |
| D.4 | Host schedule live | 7 days | Nightly off / morning WoL works; Trivy/Renovate windows respected |
| D.5 | Weather `notify` through real storms | Until ≥ 2 storms | Alerts match reality; tune AS3935 noise floor and corroboration (closes **M5**) |
| D.6 | Weather `enforce` | Next storm season | Warning countdown and Danger shutdown fire; all-clear brings the host back (closes **M7**) |
| D.7 | `systemd-analyze security labpower` | — | Exposure ≤ 2.0 (closes **M8**) |

Also: test a real power cut. With BIOS set to "Stay off", check that the Pi
reboots, the clock stays trusted via the RTC, and the host is woken according
to schedule and weather.

### Phase E: LabyrinthStack changes (separate repo, separate PRs)

**Prepared:** `docs/LABYRINTHSTACK.md` is ready to file as an issue there.
It covers:

- the exact Proxmox privilege and ACL path for every API call labpower
  makes, with `pveum` reference commands;
- tofu guest settings;
- the Ansible role, including release-asset checksum verification and
  removing `fake-hwclock`;
- the Proxmox host, the firewall (with an nftables sketch), Pangolin, and
  CI.

labpower now shows token-hidden always-on guests as "Not visible to
labpower" instead of "not found". Doing the work itself needs that repo.

Tracked there, not here. Checklist from CLAUDE.md:

- [ ] OpenTofu: `labpower@pve` user/role/token with per-guest ACLs (scheduled guests only); `on_boot`; `ignore_changes = [started]`
- [ ] Ansible: Pi in inventory; `roles/labpower` (binary, config, credentials, unit); Pi hardening; Newt as a native service; `site.yml` tolerates powered-off hosts
- [ ] Proxmox: persistent `ethtool … wol g`; BIOS WoL on, ErP off, "Stay off" on AC loss
- [ ] Firewall: router + Pi nftables per the table in CLAUDE.md
- [ ] Forgejo Actions: arm64 build on tag, checks, release asset + checksum; Renovate

### Phase F: remote access ("manage it away from home")

**Done locally (2026-09-29):**

- **`systemd-analyze security` (M8):** the unit went from 2.3 to **1.5**
  (target ≤ 2.0), checked with `--offline=true`.
  - Added `ProtectClock`, `ProtectKernelLogs`, `ProtectHostname`,
    `ProtectProc=invisible`, `UMask=0077`, `DevicePolicy=closed`,
    `RestrictSUIDSGID`, and `SystemCallFilter=~@privileged @resources`
    with `SystemCallErrorNumber=EPERM`.
  - Fixed `PrivateDevices=no  # …`, which was silently ignored because
    systemd has no trailing comments.
  - labpower was run under the same seccomp, address-family, W^X and
    umask settings (`systemd-run --user`) and served, ticked and wrote its
    DB normally.
  - Re-check on the Pi itself, since its systemd version may differ.
- **OWASP ZAP baseline (M6):** run against a local instance, signed out
  and signed in (session cookie injected): **no medium or high findings**.
  - Fixed the Low findings: added COOP/CORP/COEP headers.
  - Fixed unknown paths (e.g. `/robots.txt`) serving the dashboard; they
    now return 404.
  - Remaining: informational only (`no-store` on pages is intended), plus
    htmx's `eval` code path, which is disabled.

**Still to do (needs the deployment):**

1. Pangolin **private** resource (client access only), owner-only role, 2FA
   enforced, **no auth-bypass rules**.
2. Re-run the ZAP baseline against the Pangolin URL (through Pangolin's
   own auth, which adds its own headers).
3. Manual security review: auth flows, step-up coverage, rate limits,
   logs, `/healthz`.
4. Only then, optionally, go public: CrowdSec + Geoblock on Pangolin.

### Phase G: later (M9)

UPS via NUT (`shutdown_after_on_battery`) as another Safety input; Prometheus
`/metrics`; host on-hours and energy estimate on the dashboard.

---

## 6. Working agreement (unchanged from CLAUDE.md)

- Dry-run stays the default. Tests never touch real Proxmox, router, or weather
  services.
- Before every commit: `gofmt -l .` (empty), `go vet ./...`,
  `staticcheck ./...`, `go test -race ./...`, `govulncheck ./...`.
- Security requirements are requirements. If one conflicts with another, stop
  and ask.
- Update this file when a phase item lands.
