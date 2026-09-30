# LabyrinthStack changes for labpower

Ready to paste as an issue (or split into PRs) in the **LabyrinthStack**
repo. Nothing here is done in this repo (CLAUDE.md, "LabyrinthStack
changes"). `<…>` values are owner decisions (PLAN.md §4). The OpenTofu
shapes are provider-agnostic; the `pveum` commands are the reference for
what must exist.

---

## 1. Proxmox: user, role, token, ACLs (OpenTofu `tofu/prod/`)

Every API call labpower makes and the privilege it needs:

| labpower call | Endpoint | Privilege | Path |
|---|---|---|---|
| list guests | `GET /cluster/resources?type=vm` | `VM.Audit` (filters the list) | `/vms/<vmid>` per scheduled guest |
| start / shut down guest | `POST /nodes/<node>/{qemu,lxc}/<vmid>/status/{start,shutdown}` | `VM.PowerMgmt` | `/vms/<vmid>` per scheduled guest |
| host reachable | `GET /nodes/<node>/status` | `Sys.Audit` | `/nodes/<node>` |
| active tasks (backups) | `GET /nodes/<node>/tasks?source=active` | `Sys.Audit` (to see other users' tasks) | `/nodes/<node>` |
| task status | `GET /nodes/<node>/tasks/<upid>/status` | own tasks, or `Sys.Audit` | `/nodes/<node>` |
| ZFS scrub pre-check | `GET /nodes/<node>/disks/zfs[/<pool>]` | `Sys.Audit` | `/nodes/<node>` |
| host shutdown | `POST /nodes/<node>/status` `command=shutdown` | `Sys.PowerMgmt` | `/nodes/<node>` |

**Always-on guests (`lxc-forge`, `lxc-edge`) get no ACL at all**, so even
a compromised Pi can't stop them (the labpower UI shows them as "Not
visible to labpower", which is expected).

Reference commands (what the OpenTofu resources must produce):

```sh
pveum role add LabpowerGuest -privs "VM.Audit VM.PowerMgmt"
pveum role add LabpowerNode  -privs "Sys.Audit Sys.PowerMgmt"
pveum user add labpower@pve --comment "labpower on the Pi (no password)"
pveum user token add labpower@pve pi --privsep 1     # prints the secret once

# privsep=1: effective rights are user ∩ token, so grant both.
for who in "-users labpower@pve" "-tokens labpower@pve!pi"; do
  pveum acl modify /nodes/<node> $who -roles LabpowerNode
  for vmid in <scheduled vmids, e.g. 201>; do
    pveum acl modify /vms/$vmid $who -roles LabpowerGuest
  done
done
```

The token secret goes into vault-encrypted `host_vars` for the Pi (§2), and
never into this repo.

Guest resources:

- Scheduled guests: `on_boot = false`, and `lifecycle { ignore_changes = [started] }`
  (labpower owns their power state, so tofu must not fight it).
- `lxc-forge`, `lxc-edge`: `on_boot = true`.
- Optional: a `labpower` / schedule-name tag on scheduled guests for
  visibility in the Proxmox UI.

## 2. Ansible: the Pi and `roles/labpower`

**Deployment is Docker (D8):** use the ready role in this repo,
`deploy/ansible/roles/labpower`, with the image Forgejo pushes to the
registry (see deploy/README.md). It covers the config, secrets, state dir,
registry login and the hardened container. The systemd notes below apply
only to the alternative install. The Pi still needs Docker, the RTC/I2C
setup, no `fake-hwclock`, and Newt.

- Inventory: add the Pi (`<pi-ip>`, VLAN 70) to `ansible/inventory/hosts`.
- `roles/labpower`:
  - Download the release asset `labpower-<version>-linux-arm64` and
    **verify its `.sha256`** before installing to `/usr/local/bin/labpower`
    (0755).
  - `/etc/labpower/config.yaml` (0600 root) from a template. Its values:
    D1–D10, `dry_run: true` until the rollout says otherwise, and
    `clock.trust: ntp_or_rtc`.
  - `/etc/labpower/{proxmox-token,notify-token}` (0600 root) from vault.
  - `deploy/labpower.service` from this repo, installed verbatim. It loads
    the config and both secrets with `LoadCredential=`; the DynamicUser
    can't read `/etc/labpower` directly.
  - `systemctl daemon-reload`, `enable --now labpower`. Handler: restart
    on config/binary change.
  - Check: `systemd-analyze security labpower` ≤ 2.0 (1.5 measured
    offline).
- Pi base hardening:
  - SSH keys only; unattended-upgrades; log2ram or a read-only overlay.
  - `dtoverlay=i2c-rtc,ds3231` and `dtparam=i2c_arm=on`; **remove
    `fake-hwclock`**, since labpower trusts the clock only via NTP or the
    RTC.
  - WiFi off (it disturbs the AS3935).
- Newt as a native systemd service with its own Pangolin site, forwarding
  to `127.0.0.1:8080`.
- `site.yml`: plays must tolerate scheduled hosts being powered off
  (e.g. `ignore_unreachable` or a reachability pre-check for guests
  labpower schedules).

## 3. Proxmox host

- Persistent Wake-on-LAN in `/etc/network/interfaces`:
  `post-up /usr/sbin/ethtool -s <nic> wol g`.
- BIOS: WoL on, ErP off, "Restore on AC power loss: **Stay off**"
  (labpower decides when it boots after an outage).
- Router WoL relay into VLAN 10 per the chosen method (D3): a static ARP
  entry for `unicast`, or the router's WoL API for `router_api`.

## 4. Firewall (router + Pi nftables)

The Pi is in VLAN 70. Default deny in both directions.

| Direction | Allow |
|---|---|
| Pi → Proxmox `<proxmox-ip>` | TCP 8006 |
| Pi → router | WoL: UDP 9 to the static-ARP IP, or the router API port (D3) |
| Pi → vps-bastion | Pangolin/Gerbil tunnel ports (`TODO(owner)`) |
| Pi → internet | TCP 443 (Open-Meteo, MeteoAlarm, ntfy, lightning API) |
| Pi → router | DNS (UDP/TCP 53), NTP (UDP 123) |
| admin → Pi | TCP 22 |

Pi `nftables.conf` sketch (fill in the addresses):

```nft
table inet filter {
  chain input {
    type filter hook input priority 0; policy drop;
    iif lo accept
    ct state established,related accept
    ip saddr <admin-subnet> tcp dport 22 accept
  }
  chain output {
    type filter hook output priority 0; policy drop;
    oif lo accept
    ct state established,related accept
    ip daddr <proxmox-ip> tcp dport 8006 accept
    ip daddr <wol-target> udp dport 9 accept
    ip daddr <router-ip> udp dport { 53, 123 } accept
    ip daddr <router-ip> tcp dport 53 accept
    tcp dport 443 accept
    # Pangolin/Gerbil tunnel: TODO(owner)
  }
}
```

## 5. Pangolin

- A dedicated site for the Pi's Newt.
- labpower as a **private** resource first (Pangolin client access),
  owner-only role, 2FA enforced, **no auth-bypass rules**.
- The resource hostname is `public_url` / `auth.rp_id` (D10). Passkeys are
  bound to it, so decide it before enrolling.
- Only after the manual security review: public, with CrowdSec and
  Geoblock.

## 6. CI (Forgejo Actions) and Renovate

- This repo already has `.forgejo/workflows/{ci,release}.yml`: checks on
  every push, arm64 binary + `.sha256` as release assets on `v*` tags.
  The Ansible role consumes those assets.
- Renovate: track Go modules in this repo, and the pinned labpower
  version in LabyrinthStack.
