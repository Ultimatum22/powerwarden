# Deploying labpower

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
