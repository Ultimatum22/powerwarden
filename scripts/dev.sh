#!/usr/bin/env bash
# Local development loop (`make dev`): runs cmd/fakepve and `labpower serve`
# against it, entirely on loopback. State lives in .dev/ (gitignored);
# `make dev-reset` wipes it.
#
# Env: LIVE=1 sets dry_run: false, so labpower's actions change the fake's
# state (guests start/stop, host shuts down and wakes). Without it labpower
# only logs what it would do, exactly as on a fresh deployment.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dev=$root/.dev
bin=$root/bin/dev

# A second instance would fail to bind, yet its health check would reach
# the first one and report success, so refuse up front.
for port in 8006 8007 8080; do
	if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
		echo "dev: port $port is already in use (is make dev already running?)" >&2
		exit 1
	fi
done

mkdir -p "$dev/state"
chmod 700 "$dev"
[[ -f $dev/proxmox-token ]] || { umask 077; printf 'dev-secret' > "$dev/proxmox-token"; }

pids=()
cleanup() {
	trap - EXIT INT TERM
	((${#pids[@]})) && kill "${pids[@]}" 2>/dev/null
	wait 2>/dev/null
	echo
	echo "dev: stopped."
}
trap cleanup EXIT INT TERM

rm -f "$dev/fingerprint"
"$bin/fakepve" -dir "$dev" -token-id 'labpower@pve!pi' -token-file "$dev/proxmox-token" &
pids+=($!)

for _ in {1..50}; do [[ -s $dev/fingerprint ]] && break; sleep 0.1; done
[[ -s $dev/fingerprint ]] || { echo "dev: fakepve did not start" >&2; exit 1; }

if [[ ! -f $dev/config.yaml ]]; then
	sed -e "s|@DEV_DIR@|$dev|g" \
		-e "s|@FINGERPRINT@|$(tr -d '\n' < "$dev/fingerprint")|" \
		-e "s|@DRY_RUN@|true|" \
		"$root/deploy/config.dev.yaml" > "$dev/config.yaml"
fi
if [[ ${LIVE:-0} == 1 ]]; then dry_run=false; else dry_run=true; fi
sed -i.bak "s|^dry_run: .*|dry_run: $dry_run|" "$dev/config.yaml" && rm -f "$dev/config.yaml.bak"

"$bin/labpower" check-config -config "$dev/config.yaml" >/dev/null
"$bin/labpower" serve -config "$dev/config.yaml" -state-dir "$dev/state" &
pids+=($!)

for _ in {1..50}; do curl -fs http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.1; done

cat <<EOF

────────────────────────────────────────────────────────────────────
 labpower dev   dry_run: $dry_run   (LIVE=1 make dev to act for real)

 UI              http://localhost:8080      use localhost, not 127.0.0.1
 fake Proxmox    http://127.0.0.1:8007      status + simulation controls
 config          .dev/config.yaml           edit, then restart make dev
EOF
if enrol_out=$("$bin/labpower" enrol -config "$dev/config.yaml" -state-dir "$dev/state" 2>/dev/null); then
	echo
	echo " First run: register a passkey at"
	echo "   $(grep -o 'http[^ ]*/enrol?token=[^ ]*' <<<"$enrol_out")"
	echo " (valid 15 min; \`make dev-enrol\` prints a new one)"
fi
cat <<EOF
────────────────────────────────────────────────────────────────────

EOF

wait -n "${pids[@]}"
