# Deploying labpower

For local development without any hardware, see "Local development" at
the end of this file.

Two ways to run labpower; pick one per host. **The homelab uses Docker**
(registry image + Ansible); the systemd unit remains a supported
alternative.

## Docker (the homelab deployment)

### 1. Images: Forgejo CI → your registry

`.forgejo/workflows/image.yml` builds the image with [ko](https://ko.build)
(straight from the Go source: no Docker daemon or QEMU in CI, Go
cross-compiles) on a distroless `nonroot` base pinned by digest in
`.ko.yaml`, for linux/arm64 (the Pi) and linux/amd64. The full check suite
runs first; nothing is pushed if it fails.

Like homelab-new's `build-images.yml`, the runner doesn't talk to the
registry itself: it copies the built OCI layout to vm-registry over SSH and
pushes it from there to `localhost:5555` (the registry itself has no auth).
Set this in Forgejo → repository → Settings → Actions:

| Kind | Name | Value |
|---|---|---|
| Secret | `ANSIBLE_SSH_PRIVATE_KEY` | SSH key for `homelabuser@vm-registry` (same one homelab-new uses) |

| Trigger | Tags pushed to `10.22.40.24:5555/labpower` |
|---|---|
| push to `main` | `main`, `sha-<commit>` |
| tag `vX.Y.Z` | `vX.Y.Z`, `X.Y`, `latest` |
| other `v*` tags (e.g. `v1.5.0-rc1`) | only that tag |

The job log ends with the image digest; pin that in Ansible for
reproducible deploys. Release tags also still publish the plain arm64
binary (`release.yml`), for the systemd path.

### 2. Deploy: Ansible role

`deploy/ansible/roles/labpower` is a self-contained role for your homelab
playbook (needs the `community.docker` collection). Copy it into your
roles path or reference this directory, then:

```yaml
- hosts: labpower_pi
  become: true
  roles:
    - role: labpower
      vars:
        labpower_registry: <registry host[:port]>
        labpower_image: "<registry>/labpower:v1.2.3"   # or …/labpower@sha256:<digest>
        labpower_registry_username: "{{ vault_registry_pull_user }}"   # omit for anonymous pulls
        labpower_registry_password: "{{ vault_registry_pull_password }}"
        labpower_proxmox_token: "{{ vault_labpower_proxmox_token }}"
        labpower_notify_token: "{{ vault_labpower_notify_token }}"
        labpower_sensor_enabled: true            # AS3935 on /dev/i2c-1 + GPIO
        labpower_config:                         # config.yaml as YAML; see config.example.yaml
          dry_run: true
          timezone: Europe/Amsterdam
          # … the rest of config.example.yaml …
          proxmox:
            token_secret_file: ${CREDENTIALS_DIRECTORY}/proxmox-token
            # …
```

It writes `/etc/labpower/config.yaml` and `/etc/labpower/secrets/*` owned
by the image's user (65532, mode 0400), creates `/var/lib/labpower`, logs
in to the registry if credentials are given, and runs the container with:

- **`network_mode: host`**: labpower binds `127.0.0.1` only and is reached
  only through Newt on the same host. Host networking keeps that loopback
  the host's; a bridge network with published ports would not (CLAUDE.md's
  core security requirement).
- **Hardening** equivalent to the systemd unit: read-only root filesystem,
  all capabilities dropped, `no-new-privileges`, non-root user, 150 MB
  memory and 64-process limits, logs to journald.
- **`/run/systemd/timesync` mounted read-only**, so `clock.trust:
  ntp_or_rtc` can see timesyncd's sync marker; the RTC flag is visible via
  `/sys`. Without it only the RTC can make the clock trusted.
- The sensor's devices and the host's `i2c`/`gpio` group IDs (looked up
  with getent) when `labpower_sensor_enabled`.

Config or secret changes restart the container. To upgrade, change
`labpower_image`.

Everyday commands on the Pi:

```sh
docker logs -f labpower
docker exec labpower /ko-app/labpower status -config /etc/labpower/config.yaml
docker exec labpower /ko-app/labpower enrol  -config /etc/labpower/config.yaml   # first run: prints the 15-min link
```

`docker exec` runs as the container's user against its live state, so
enrolment leaves no root-owned database files behind.

Not verified yet on the Pi itself: the AS3935 inside the container.
Docker masks `/sys/firmware`, where periph.io may look for the Pi's device
tree. If the sensor isn't detected (`weather: local sensor unavailable` in
the logs), add a read-only mount of `/sys/firmware/devicetree/base`.

### Docker Compose (by hand)

`../docker-compose.yml` runs the same container for trying things out:
`docker compose up -d --build` builds locally from `../Dockerfile`, and
`LABPOWER_IMAGE=<registry>/labpower:v1.2.3 docker compose up -d` runs the
published image. The file lists the one-time setup (config, secrets and
their ownership). There's no Docker `HEALTHCHECK`: the image has no shell
or curl, and `labpower status` would report "unhealthy" whenever the
Proxmox host is legitimately off.

## systemd (alternative)

`labpower.service` is the hardened unit (exposure 1.5 in
`systemd-analyze security`). Manually:

```
install -Dm755 labpower /usr/local/bin/labpower
install -Dm644 deploy/labpower.service /etc/systemd/system/labpower.service
install -Dm600 config.yaml /etc/labpower/config.yaml   # passed to the service via LoadCredential
install -Dm600 secrets/proxmox-token /etc/labpower/proxmox-token
install -Dm600 secrets/notify-token /etc/labpower/notify-token
systemctl daemon-reload
systemctl enable --now labpower
```

`config.yaml`'s `token_secret_file: ${CREDENTIALS_DIRECTORY}/proxmox-token`
works unmodified under both deployments: systemd's `LoadCredential=` sets
that variable, and the container sets it to `/run/secrets`.

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
