# Testing labpower against the real Proxmox host (dry-run)

How to run labpower from a laptop against the production Proxmox host
without changing anything on it. labpower stays in **dry-run**: it only
reads from Proxmox and logs what it *would* do (`dry-run: would start
guest …`).

This is a one-off check before the Pi rollout. It does not replace the Pi
itself: Wake-on-LAN across VLANs, the Pi's firewall rules and the AS3935
sensor are only tested there (see labpower's `docs/ROLLOUT.md`, stages 0–1).

> **Keep `dry_run: true` for the whole test.** With `dry_run: false` both
> the scheduler and the CLI (`guest start|stop`, `wake`, `host shutdown`)
> really power guests and the host on and off. The laptop must never be the
> instance that runs the power schedule.

## Prerequisites

- A checkout of the labpower repository, with Docker and Docker Compose.
- Network access from the laptop to `<proxmox-ip>:8006` (management VLAN
  `10.22.10.0/24`).
- Root shell on the Proxmox host (for the fingerprint and, if needed, the
  API token).

## 1. Collect the host's values

| Value | How to get it |
|---|---|
| Node name | `hostname` on the Proxmox host |
| IP address | the host's address in `10.22.10.0/24` |
| TLS fingerprint (SHA-256) | on the host: `pvenode cert info \| grep -i sha256` |

Alternatively, fetch the fingerprint from the laptop. This trusts the
network path, so compare it with the one shown in the Proxmox web UI
(*Node → System → Certificates*):

```sh
openssl s_client -connect <proxmox-ip>:8006 </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256
```

## 2. Create the API token (if it doesn't exist yet)

The intended way is the OpenTofu change in LabyrinthStack (`labpower@pve`
user, roles, token and per-guest ACLs). To try it before that lands, run on
the Proxmox host:

```sh
pveum user add labpower@pve
pveum role add LabpowerGuest -privs "VM.Audit VM.PowerMgmt"
pveum role add LabpowerNode  -privs "Sys.Audit Sys.PowerMgmt"
pveum acl modify /vms/<vmid-of-vm-media> -user labpower@pve -role LabpowerGuest
pveum acl modify /nodes/<node>           -user labpower@pve -role LabpowerNode
pveum user token add labpower@pve pi --privsep 0   # prints the secret once: copy it
```

Add one `acl modify /vms/<vmid>` line per scheduled guest. **Do not** grant
anything on `lxc-forge` or `lxc-edge`: labpower must not be able to stop
the always-on guests, even if the Pi is compromised.

## 3. Write the config and secret

In the root of the labpower checkout (`config.yaml` and `secrets/` are
gitignored):

```sh
cp deploy/config.example.yaml config.yaml
mkdir -p secrets && printf '%s' '<token secret>' > secrets/proxmox-token
chmod 0444 config.yaml secrets/* && chmod 0755 secrets
```

`chmod 0444` lets the container's non-root user read the files without a
`chown 65532`. That shortcut is for this laptop test only; on the Pi use
`chown 65532:65532` and mode `0400`.

Edit `config.yaml`:

- `dry_run: true` (must stay).
- `proxmox.url: https://<proxmox-ip>:8006`, `proxmox.node: <node>`,
  `proxmox.tls_fingerprint: "<fingerprint>"`.
- `guests:` only the guests the token was granted, plus the always-on
  ones marked `always_on: true`.
- `notify.url`: a throwaway topic, e.g. `https://ntfy.sh/<random-string>`.
  `notify.token_file` is optional; remove it if the topic is public.
- `weather.mode: notify`, or remove the weather providers entirely.

## 4. Run and check

```sh
docker compose up -d --build
docker exec labpower /ko-app/labpower check-config
docker exec labpower /ko-app/labpower status     # read-only
docker compose logs -f                           # watch the "dry-run: would …" lines
```

Expected from `status`:

- The host is reported as reachable.
- Only the guests the token may audit are listed. `lxc-forge` and
  `lxc-edge` are **absent**, which confirms the token is scoped correctly.

## 5. Clean up

```sh
docker compose down -v
rm -rf config.yaml secrets        # removes the token secret from the laptop
```

If the token was created only for this test, revoke it on the host:

```sh
pveum user token remove labpower@pve pi
```
