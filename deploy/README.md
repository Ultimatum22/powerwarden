# Deploying labpower

For local development without any hardware, see "Local development" at
the end of this file.

Two ways to run labpower; pick one per host.

## systemd (the primary, documented deployment)

`labpower.service` is the hardened unit CLAUDE.md specifies, meant to be
installed by the LabyrinthStack repo's Ansible role. Manually:

```
install -Dm755 labpower /usr/local/bin/labpower
install -Dm644 deploy/labpower.service /etc/systemd/system/labpower.service
install -Dm600 config.yaml /etc/labpower/config.yaml
install -Dm600 secrets/proxmox-token /etc/labpower/proxmox-token
install -Dm600 secrets/notify-token /etc/labpower/notify-token
systemctl daemon-reload
systemctl enable --now labpower
```

## Docker / Docker Compose

`../Dockerfile` and `../docker-compose.yml` at the repo root package the
same binary into a minimal, non-root, distroless image. Useful for
running on a host you don't want to (or can't) manage with systemd, or
for local testing against a fake Proxmox server without touching the
real one.

```
cp deploy/config.example.yaml config.yaml   # then fill in every TODO(owner)
mkdir -p secrets
echo -n '<proxmox token secret>' > secrets/proxmox-token
echo -n '<ntfy token>'           > secrets/notify-token
docker compose up -d --build
```

Two things this setup deliberately preserves from the systemd version,
rather than taking the usual Docker shortcut:

- **`network_mode: host`**, not a bridge network + `ports:` mapping.
  CLAUDE.md's core security requirement is that labpower binds to
  `127.0.0.1` only and is reachable exclusively through Newt/Pangolin.
  Host networking is what makes that true in a container the same way
  it's true for the systemd deployment — the container's `127.0.0.1` *is*
  the host's `127.0.0.1`. A bridge network's loopback is a different,
  isolated thing; don't switch to one without re-reading why
  `config.yaml`'s `listen` is validated as loopback-only.
- **An init container fixes the state volume's ownership** to the
  distroless image's `nonroot` UID/GID (65532) before labpower starts. A
  fresh named volume is created root-owned, and the labpower image has no
  shell to `chown` it from the inside (that's also why there's no Docker
  `HEALTHCHECK`: no curl/wget to call `GET /healthz` with, and
  `labpower status` isn't a substitute — it calls out to Proxmox, so it'd
  report "unhealthy" whenever the host is legitimately off).

The AS3935 sensor's `/dev/i2c-1` and `/dev/gpiochip0` device mappings and
their `group_add` GIDs are Raspberry Pi OS's usual values — check
`getent group i2c gpio` on the actual host and adjust, or delete both if
`weather.local_sensor.enabled: false`.

`config.yaml`'s `token_secret_file: ${CREDENTIALS_DIRECTORY}/proxmox-token`
works unmodified under either deployment: systemd's `LoadCredential=`
sets that env var itself, and `docker-compose.yml` sets it to `/run/secrets`
to match where Compose secrets mount.

## Local development

`make dev` runs labpower against `cmd/fakepve`, a stand-in for the Proxmox
host, entirely on loopback. No real Proxmox, router, weather, or ntfy
service is contacted.

```
make dev          # dry-run: labpower only logs what it would do
LIVE=1 make dev   # dry_run: false: guests start/stop, host shuts down/wakes (all fake)
make dev-enrol    # new enrolment link (15 min) until a passkey is registered
make dev-reset    # wipe .dev/ (passkeys, sessions, overrides, events, config)
```

On the first run it prints an enrolment link. Open it in Chrome or
Firefox **at `http://localhost:8080`** (not 127.0.0.1: WebAuthn is bound
to `localhost`) and register a passkey. Plain http is accepted only
because the host is loopback; config validation rejects http anywhere
else.

fakepve serves:

| Address | What |
|---|---|
| `https://127.0.0.1:8006` | Proxmox API subset, self-signed cert pinned via `.dev/fingerprint` |
| `udp://127.0.0.1:40009` | Wake-on-LAN target: a magic packet for `aa:bb:cc:dd:ee:ff` boots the host |
| `http://127.0.0.1:8007/` | status page; ntfy sink (notifications appear in the log as `NOTIFY`) |

Simulate things that happen outside labpower:

```
curl -X POST 127.0.0.1:8007/control/guests/vm-media/start   # start from the Proxmox UI
curl -X POST '127.0.0.1:8007/control/backup?for=5m'         # active task, postpones shutdown
curl -X POST '127.0.0.1:8007/control/scrub?for=10m'         # ZFS scrub, fails the shutdown pre-check
curl -X POST 127.0.0.1:8007/control/host/off                # power cut
curl -X POST 127.0.0.1:8007/control/host/on                 # power button
```

The rendered config is `.dev/config.yaml`. Edit it (schedules, guests,
weather providers) and restart `make dev`.
