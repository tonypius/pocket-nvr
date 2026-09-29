#!/usr/bin/env bash
# Install a dashboard-created Cloudflare Tunnel token on the phone
# (Design B, Method A — deploy/cloudflare/README.md). This is the
# login-free path: no `cloudflared tunnel login`, no CLI DNS routing —
# the tunnel and its public hostname are managed in the Zero Trust
# dashboard; this script only installs the token and starts the daemon.
#
# Usage: scripts/cloudflare-token.sh <token>   (or a file containing it)
#   Token comes from Zero Trust → Networks → Tunnels → Create tunnel →
#   "Install and run a connector" → the eyJ... string.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
BASE=/data/nvr
STAGE=/data/local/tmp/pocketnvr

[[ $# -eq 1 ]] || { echo "usage: $0 <token | token-file>"; exit 1; }
command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
STATE=$(adb get-state 2>/dev/null || echo none)
[[ "$STATE" == "device" ]] || { echo "no authorized device (state: $STATE)"; exit 1; }
adb shell su -c id | grep -q "uid=0" || { echo "su not elevated — grant Shell superuser in Magisk"; exit 1; }

if [ -f "$1" ]; then token=$(tr -d ' \n\r' <"$1"); else token=$(printf '%s' "$1" | tr -d ' \n\r'); fi
[[ -n "$token" ]] || { echo "empty token"; exit 1; }
case "$token" in eyJ*) ;; *) echo "note: dashboard tokens normally start with eyJ — continuing";; esac

printf '%s' "$token" > "$DIST/cf_token.txt"
adb shell "mkdir -p $STAGE" >/dev/null
adb push "$DIST/cf_token.txt" "$STAGE/cf_token.txt" >/dev/null
rm "$DIST/cf_token.txt"

# Multiline device commands must be pushed as a script file: inside
# `adb shell su -c "a && b"` the && parsing happens OUTSIDE su, so only
# the first command runs as root (repo convention, see scripts/deploy.sh).
cat > "$DIST/cf_token_install.sh" <<'EOF'
#!/system/bin/sh
set -e
BASE=/data/nvr
STAGE=/data/local/tmp/pocketnvr
mkdir -p $BASE/cloudflare
cp $STAGE/cf_token.txt $BASE/cloudflare/token.txt
chmod 600 $BASE/cloudflare/token.txt
rm $STAGE/cf_token.txt
if ! $BASE/bin/nvrctl restart tunnel; then
	echo "nvrctl rejected 'tunnel' — run scripts/deploy.sh to refresh nvrctl on the phone"
	exit 1
fi
sleep 2
$BASE/bin/nvrctl status tunnel
echo TOKEN_INSTALLED
EOF
adb push "$DIST/cf_token_install.sh" "$STAGE/cf_token_install.sh" >/dev/null

set +e
OUT=$(adb shell su -c "sh $STAGE/cf_token_install.sh" 2>&1 | tr -d '\r')
set -e
echo "$OUT"
echo "$OUT" | grep -q TOKEN_INSTALLED || { echo "== token install FAILED (see output above)"; exit 1; }

cat <<'NEXT'

== tunnel installed. Finish in the Zero Trust dashboard:
   1. The tunnel you created -> Public hostname tab -> add:
        subdomain: nvr    domain: yourdomain.com    service: HTTP  localhost:8099
      (this creates the DNS record — nothing to run on the phone)
   2. Access -> Applications -> Add -> Self-hosted  ->  nvr.yourdomain.com
      Policy: Allow / Include / Emails — the people you approve.
      Login settings: session duration + return 401 on expired sessions.
   3. config.yaml: notifications.base_url: https://nvr.yourdomain.com
   Full walkthrough: deploy/cloudflare/README.md (Method A)
NEXT
