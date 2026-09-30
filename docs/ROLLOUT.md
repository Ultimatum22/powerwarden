# Rollout runbook: labpower on the Pi

Step-by-step for PLAN.md Phase D, from a blank Pi to weather enforcement.
Each stage has a soak time and an acceptance check. **Rollback is always
`dry_run: true` in the config, then `sudo systemctl restart labpower`.**

Placeholders like `<pi>` or `<proxmox-ip>` are the owner's values (PLAN.md §4,
decisions D1–D10). The LabyrinthStack Ansible role (docs/LABYRINTHSTACK.md)
automates stages 1–2; the manual commands are here so each step can be
checked by hand the first time.

---

## Stage 0: prerequisites on the Pi

Raspberry Pi OS Lite **32-bit (armhf)**, Ethernet (WiFi off, it disturbs the
AS3935), SSH keys only.

```sh
# RTC + I2C (DS3231 via the kernel; labpower never sets the clock itself)
echo 'dtparam=i2c_arm=on'            | sudo tee -a /boot/firmware/config.txt
echo 'dtoverlay=i2c-rtc,ds3231'      | sudo tee -a /boot/firmware/config.txt
sudo apt purge -y fake-hwclock       # would restore a stale time after power loss
sudo reboot
```

Check, after the reboot:

| Check | Command | Expect |
|---|---|---|
| RTC set the clock at boot | `cat /sys/class/rtc/rtc0/hctosys` | `1` |
| NTP | `timedatectl show -p NTPSynchronized` | `NTPSynchronized=yes` |
| timesyncd marker (what labpower reads) | `ls /run/systemd/timesync/synchronized` | exists |
| sensor on the bus | `sudo i2cdetect -y 1` | `03` (AS3935) and `UU`/`68` (RTC) |
| groups for the unit | `getent group i2c gpio` | both exist |
| Pi → Proxmox API | `curl -sk https://<proxmox-ip>:8006/api2/json/version -o /dev/null -w '%{http_code}\n'` | `401` (reachable, no token) |

labpower only acts once one of the first three holds (`clock.trust:
ntp_or_rtc`, the default).

## Stage 1: install, dry-run (7 days) — closes M2

**Docker (the chosen deployment, D8):** cut a release (Actions →
Release → Run workflow on main) so Forgejo tags it and pushes the image,
then run your playbook
with `deploy/ansible/roles/labpower`, `dry_run: true` in `labpower_config`.
In the stages below, replace `lp <cmd> …` with
`docker exec labpower /ko-app/labpower <cmd> -config /etc/labpower/config.yaml`
and `journalctl -u labpower` with `docker logs labpower` (journald also has
it). Changing the config means changing the playbook vars and re-running;
the role restarts the container. The systemd commands below remain valid
for the alternative install.

On the build machine (never on the Pi):

```sh
make release VERSION=v0.1.0          # runs every check, then builds armv7
scp bin/labpower-v0.1.0-linux-armv7{,.sha256} <pi>:
```

On the Pi:

```sh
sha256sum -c labpower-v0.1.0-linux-armv7.sha256
sudo install -Dm755 labpower-v0.1.0-linux-armv7 /usr/local/bin/labpower
sudo install -Dm644 labpower.service /etc/systemd/system/labpower.service
sudo install -Dm600 config.yaml   /etc/labpower/config.yaml     # dry_run: true, weather.mode: notify
sudo install -Dm600 proxmox-token /etc/labpower/proxmox-token   # token secret only
sudo install -Dm600 notify-token  /etc/labpower/notify-token

sudo env CREDENTIALS_DIRECTORY=/etc/labpower labpower check-config -config /etc/labpower/config.yaml   # fix every error; read every warning
sudo systemd-analyze security labpower                     # expect <= 2.0 (1.5 offline on dev)
sudo systemctl daemon-reload && sudo systemctl enable --now labpower
journalctl -u labpower -f
```

The service gets `config.yaml` and both secrets through `LoadCredential=`
(it runs as a DynamicUser, which can't read the root-only files in
`/etc/labpower`). The config's `${CREDENTIALS_DIRECTORY}/…` paths therefore
need that variable when you run the CLI yourself as root:

```sh
lp() { sudo env CREDENTIALS_DIRECTORY=/etc/labpower /usr/local/bin/labpower "$@"; }
lp check-config -config /etc/labpower/config.yaml
lp status       -config /etc/labpower/config.yaml
journalctl -u labpower --since today | grep -E 'dry-run|host|weather'
```

(The web UI comes with Pangolin, Stage 7; until then use the CLI over SSH.)

**Accept:** after 7 days, every `dry-run: would …` line matches the
schedules (starts/stops at each boundary, dependency order, nothing for
`lxc-forge`/`lxc-edge`), the Trivy/Renovate windows have the host on, and
there are no `engine tick failed` errors. A reboot of the Pi mid-week
applies missed boundaries once, not replayed.

## Stage 2: one real host cycle — closes M4

Still `dry_run: true` for the scheduler; the CLI is live only with `-yes`
and `dry_run: false`, so flip it, do the cycle, flip back:

```sh
sudo sed -i 's/^dry_run: true/dry_run: false/' /etc/labpower/config.yaml
sudo systemctl stop labpower          # keep the scheduler out of the way
lp host shutdown -config /etc/labpower/config.yaml          # read the summary
lp host shutdown -config /etc/labpower/config.yaml -yes     # waits for each guest
# wait until the host is off (fans stop, ping fails), then:
lp wake -config /etc/labpower/config.yaml
lp status -config /etc/labpower/config.yaml                 # repeat until reachable
sudo sed -i 's/^dry_run: false/dry_run: true/' /etc/labpower/config.yaml
sudo systemctl start labpower
```

**Accept:** guests stop cleanly in reverse dependency order, the node shuts
down, Wake-on-LAN brings it back (if not: router WoL method, D3; BIOS WoL;
`ethtool <nic> | grep Wake-on` shows `g`), notifications arrived.

## Stage 3: guests live (7 days) — closes M3

Guests are live when `dry_run: false`; the host schedule is too. To keep
the host manual for this week, give the host an always-on schedule
(`{ days: mon-sun, on: "00:00", off: "00:00" }`), then `dry_run: false`.

**Accept:** guests start/stop on schedule; a guest started by hand in the
Proxmox UI stays up until its next boundary; nothing touches the
always-on guests; events show `actor=schedule`.

## Stage 4: host schedule live (7 days)

Restore the real host schedule.

**Accept:** nightly shutdown postpones while a backup runs (and notifies
once), morning Wake-on-LAN works every day (the shutdown page's "last
successful Wake-on-LAN" stays current), Trivy/Renovate still run.

Also test a **power cut**: pull the Proxmox plug (BIOS "Stay off"), then
the Pi's. On restore the Pi boots, the clock is trusted via the RTC
(`hctosys`=1) before NTP, and the host is woken only if the schedule and
weather allow.

## Stage 5: weather notify-only (until ≥ 2 real storms) — closes M5

`weather.mode: notify` (default). Watch the Weather page and
notifications during storms.

**Accept:** levels match reality (Watch with the forecast, Warning with
official warnings / nearby strikes, Danger when close). Tune
`local_sensor.corroboration_window` and the AS3935 noise floor if the
sensor fires without storms. Without a lightning network (D6), Warning and
Danger only come from MeteoAlarm and the local sensor.

## Stage 6: weather enforce (next storm season) — closes M7

`weather.mode: enforce`; `check-config` insists on a positive
`warning.countdown` and `host.shutdown_grace`.

**Accept:** Warning shuts down after the countdown (cancellable with a
passkey from the dashboard), Danger shuts down immediately, all-clear
returns to the schedule and wakes the host.

## Stage 7: remote UI through Pangolin — PLAN.md Phase F

Passkeys are bound to `public_url`'s host (`auth.rp_id`), so **enrol only
through the real Pangolin hostname**, never through an SSH tunnel to
localhost (such a passkey would not work on the real domain).

1. Pangolin private resource for the Pi's Newt → `127.0.0.1:8080`,
   owner-only, 2FA, no auth-bypass rules.
2. **Docker:** `docker exec labpower /ko-app/labpower enrol -config /etc/labpower/config.yaml`
   (runs as the container's user against its live state).
   **systemd:** run `enrol` **as the service's dynamic user** (running it as
   root could leave root-owned SQLite `-wal`/`-shm` files the service can't
   write):

   ```sh
   sudo systemd-run --pty --wait --collect \
     -p DynamicUser=yes -p User=labpower -p StateDirectory=labpower \
     -p LoadCredential=config.yaml:/etc/labpower/config.yaml \
     -p LoadCredential=proxmox-token:/etc/labpower/proxmox-token \
     -p LoadCredential=notify-token:/etc/labpower/notify-token \
     /bin/sh -c 'exec /usr/local/bin/labpower enrol -config "$CREDENTIALS_DIRECTORY/config.yaml"'
   ```

   (Not yet run on the Pi: verify on first use.) It prints a 15-minute link;
   open it on the phone through Pangolin and register the passkey. Then add
   a second passkey (another device) under Security, and set up the
   authenticator app as a backup.
3. ZAP baseline against the Pangolin URL; manual security review.
