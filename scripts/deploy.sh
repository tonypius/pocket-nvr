#!/usr/bin/env bash
# Deploy PocketNVR to the rooted phone over adb (dev loop).
# Requires: authorized adb + Shell superuser granted in Magisk.
# Usage: scripts/deploy.sh [--start]
#
# Multiline device commands must be pushed as script files — inline
# `adb shell su -c "<multiline>"` breaks on quoting (see smoke_test_a.sh).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
BASE=/data/nvr
STAGE=/data/local/tmp/pocketnvr

command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
STATE=$(adb get-state 2>/dev/null || echo none)
[[ "$STATE" == "device" ]] || { echo "no authorized device (state: $STATE)"; exit 1; }
echo "== device: $(adb shell getprop ro.product.device | tr -d '\r') (Android $(adb shell getprop ro.build.version.release | tr -d '\r'))"

echo "== building nvrd (linux/arm64 static)"
"$ROOT/scripts/build_arm64.sh" >/dev/null
MTX="$ROOT/third_party/mediamtx/current/linux_arm64/mediamtx"
[ -x "$MTX" ] || { echo "run scripts/fetch-mediamtx.sh first"; exit 1; }
# optional Design B edge (deploy/cloudflare/): pushed only when fetched
CFL="$ROOT/third_party/cloudflared/current/linux_arm64/cloudflared"

# incident guard: a non-ELF (e.g. macOS) binary once reached the phone and
# killed the API daemon — verify magic bytes on every artifact we push
for f in "$DIST/nvrd_linux_arm64" "$MTX" "$CFL" "$ROOT/third_party/ffmpeg/ffmpeg" "$ROOT/detector/build-android-ncnn/nvrdet"; do
	[ -f "$f" ] || continue
	sig=$(head -c 4 "$f" | od -An -tx1 | tr -d ' ')
	[ "$sig" = "7f454c46" ] || { echo "REFUSING to push non-ELF artifact: $f"; exit 1; }
done

# --- device-side installer (paths are constants; kept unquoted heredoc-free)
cat > "$DIST/install_device.sh" <<'EOF'
#!/system/bin/sh
set -e
BASE=/data/nvr
STAGE=/data/local/tmp/pocketnvr
# stop daemons first: overwriting a RUNNING binary is denied (ETXTBSY)
$BASE/bin/nvrctl stop >/dev/null 2>&1 || true
mkdir -p $BASE/bin $BASE/logs $BASE/run $BASE/recordings $BASE/snapshots $BASE/clips $BASE/models $BASE/ui
rm -rf $BASE/ui/* 2>/dev/null
cp -fR $STAGE/ui/* $BASE/ui/
cp -f $STAGE/nvrd $BASE/bin/nvrd
cp -f $STAGE/mediamtx $BASE/bin/mediamtx
cp -f $STAGE/nvrctl $BASE/bin/nvrctl
chmod 755 $BASE/bin/nvrd $BASE/bin/mediamtx $BASE/bin/nvrctl
# Design B edge (optional): cloudflared + its one-time setup script
mkdir -p $BASE/cloudflare
if [ -f $STAGE/cloudflared ]; then
	cp -f $STAGE/cloudflared $BASE/bin/cloudflared
	chmod 755 $BASE/bin/cloudflared
	cp -f $STAGE/cloudflare_setup.sh $BASE/cloudflare/setup.sh
	chmod 755 $BASE/cloudflare/setup.sh
fi
# detector daemon + its decode/model dependencies (COMP-3)
if [ -f $STAGE/nvrdet ]; then
	cp -f $STAGE/nvrdet $BASE/bin/nvrdet
	chmod 755 $BASE/bin/nvrdet
	mkdir -p $BASE/models/yolo11n $BASE/bin/ffmpeg.d
	cp -f $STAGE/yolo11n.param $BASE/models/yolo11n/yolo11n.param
	cp -f $STAGE/yolo11n.bin $BASE/models/yolo11n/yolo11n.bin
	if [ -f $STAGE/ffmpeg ]; then
		cp -f $STAGE/ffmpeg $BASE/bin/ffmpeg
		chmod 755 $BASE/bin/ffmpeg
		ln -sf $BASE/bin/ffmpeg /data/local/tmp/pocketnvr-ffmpeg 2>/dev/null || true
	fi
fi
# config.yaml is device-managed once the user edits it via the UI
# (FR-API-4): seed only on first install, never clobber.
if [ ! -f $BASE/config.yaml ]; then
	sed 's|base_path: ./data|base_path: /data/nvr|' $STAGE/config/config.yaml > $BASE/config.yaml
	chmod 600 $BASE/config.yaml
fi
# secrets are device-local (FR-CFG-2): generated once, random token.
# Camera creds are placeholders until the user adds real ones.
if [ ! -f $BASE/secrets.yaml ]; then
	TOKEN=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
	VPASS=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
	cat > $BASE/secrets.yaml <<SECEOF
api_token: $TOKEN
mediamtx_viewer_pass: $VPASS
front_user: changeme
front_pass: changeme
back_user: changeme
back_pass: changeme
SECEOF
	chmod 600 $BASE/secrets.yaml
fi
	$BASE/bin/nvrd -config $BASE -check
	# verify ELF magic on-device (kernel refuses wrong-arch binaries and the
	# shell then misreads them as scripts)
	for b in nvrd mediamtx nvrctl nvrdet ffmpeg cloudflared; do
		f=$BASE/bin/$b
		[ -f "$f" ] || continue
		h=$(head -c 4 "$f" | od -An -tx1 | tr -d ' ')
		[ "$h" = "7f454c46" ] || { echo "BAD ELF: $b"; exit 1; }
	done
	echo INSTALL_OK
EOF

echo "== pushing + installing"
adb shell "rm -rf $STAGE && mkdir -p $STAGE/config" >/dev/null
adb push "$DIST/nvrd_linux_arm64" "$STAGE/nvrd" >/dev/null
adb push "$MTX" "$STAGE/mediamtx" >/dev/null
adb push "$ROOT/deploy/nvrctl" "$STAGE/nvrctl" >/dev/null
if [ -x "$CFL" ]; then
	adb push "$CFL" "$STAGE/cloudflared" >/dev/null
	adb push "$ROOT/deploy/cloudflare/setup.sh" "$STAGE/cloudflare_setup.sh" >/dev/null
else
	echo "(cloudflared not fetched — skipping Design B edge; run scripts/fetch-cloudflared.sh)"
fi
adb push "$ROOT/config/config.yaml" "$STAGE/config/config.yaml" >/dev/null
adb push "$ROOT/ui" "$STAGE/ui" >/dev/null
# detector pieces (optional: skip if not built yet)
NVDET="$ROOT/detector/build-android-ncnn/nvrdet"
FFMPEG="$ROOT/third_party/ffmpeg/ffmpeg"
MODEL_DIR="$ROOT/models/yolo11n"
if [ -x "$NVDET" ] && [ -f "$MODEL_DIR/yolo11n.param" ]; then
	adb push "$NVDET" "$STAGE/nvrdet" >/dev/null
	adb push "$MODEL_DIR/yolo11n.param" "$MODEL_DIR/yolo11n.bin" "$STAGE/" >/dev/null
	if [ -x "$FFMPEG" ]; then adb push "$FFMPEG" "$STAGE/ffmpeg" >/dev/null; fi
	# capture must find ffmpeg on PATH → prepend a known dir in start script
else
	echo "(detector not built yet — skipping COMP-3 pieces)"
fi
adb push "$DIST/install_device.sh" "$STAGE/install_device.sh" >/dev/null
adb shell su -c id | grep -q "uid=0" || { echo "su not elevated — grant Shell superuser in Magisk"; exit 1; }
adb shell su -c "sh $STAGE/install_device.sh" | grep -E "INSTALL_OK|Error|error|invalid" | tr -d '\r'

if [[ "${1:-}" == "--start" ]]; then
	cat > "$DIST/start_device.sh" <<'EOF'
#!/system/bin/sh
BASE=/data/nvr
$BASE/bin/nvrd -config $BASE -mediamtx-config $BASE/mediamtx.yml || exit 1
if [ -f $BASE/bin/nvrdet ]; then
	$BASE/bin/nvrd -config $BASE -detector-config $BASE/detector.json || exit 1
	chmod 600 $BASE/detector.json
fi
# stop-then-start: a redeploy must put the fresh binaries live
$BASE/bin/nvrctl stop
PATH=$BASE/bin:$PATH
export PATH
$BASE/bin/nvrctl start
sleep 2
$BASE/bin/nvrctl status
EOF
	adb push "$DIST/start_device.sh" "$STAGE/start_device.sh" >/dev/null
	echo "== generating mediamtx.yml + starting daemons"
	adb shell su -c "sh $STAGE/start_device.sh" | tr -d '\r'
	echo "== api token:"
	adb shell su -c "grep api_token $BASE/secrets.yaml" | tr -d '\r'
fi
echo "== deploy OK"
