#!/usr/bin/env bash
# Package and install the PocketNVR Magisk boot module on the phone, then
# (optionally) reboot to prove FR-SUP-1: daemons start unattended.
# Usage: scripts/install_boot_module.sh [--reboot]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
ZIP="$DIST/PocketNVR-boot.zip"

command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
adb get-state >/dev/null 2>&1 || { echo "no device"; exit 1; }

echo "== packaging module"
rm -rf "$DIST/module"
mkdir -p "$DIST/module"
# whole module tree: boot scripts + systemless overlays (system/etc/…)
cp -R "$ROOT/deploy/magisk-module/." "$DIST/module/"
chmod 755 "$DIST/module/service.sh" "$DIST/module/customize.sh"
(cd "$DIST/module" && zip -q -r "$ZIP" .)
unzip -l "$ZIP" | tail -6

echo "== pushing + installing via Magisk"
adb push "$ZIP" /data/local/tmp/PocketNVR-boot.zip >/dev/null
adb shell su -c "magisk --install-module /data/local/tmp/PocketNVR-boot.zip" | tr -d '\r'

if [[ "${1:-}" == "--reboot" ]]; then
	echo "== rebooting phone (FR-SUP-1 acceptance test)"
	adb reboot
	echo "waiting for device to come back (up to 3 min)..."
	adb wait-for-device
	sleep 10
	# wait for boot_completed
	for i in $(seq 1 60); do
		b=$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')
		[ "$b" = "1" ] && break
		sleep 3
	done
	echo "boot_completed=1; waiting 45s for daemons (network wait + start)..."
	sleep 45
	adb shell su -c "/data/nvr/bin/nvrctl status" | tr -d '\r'
	adb forward tcp:8099 tcp:8099 >/dev/null
	TOKEN=$(adb shell su -c "grep api_token /data/nvr/secrets.yaml" | tr -d '\r' | cut -d' ' -f2)
	curl -s --max-time 5 "http://127.0.0.1:8099/api/health?token=$TOKEN" | python3 -c 'import json,sys; h=json.load(sys.stdin); print("health:", h["status"], h["components"])'
fi
echo "== module install OK"
