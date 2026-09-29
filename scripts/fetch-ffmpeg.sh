#!/usr/bin/env bash
# Fetch a static linux/arm64 ffmpeg for the phone (detector capture pipes).
# John Van Sickle's static builds (GPL, personal use OK).
# Usage: scripts/fetch-ffmpeg.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/third_party/ffmpeg"
BASEURL=https://johnvansickle.com/ffmpeg/releases
TARBALL=ffmpeg-release-arm64-static.tar.xz

mkdir -p "$DEST"
echo "fetching $TARBALL"
curl -fL --retry 3 -o "$DEST/$TARBALL" "$BASEURL/$TARBALL"
EXTRACT="$DEST/extract"
rm -rf "$EXTRACT" && mkdir -p "$EXTRACT"
tar -xJf "$DEST/$TARBALL" -C "$EXTRACT"
mv "$EXTRACT"/*/ffmpeg "$DEST/ffmpeg"
rm -rf "$EXTRACT" "$DEST/$TARBALL"
chmod +x "$DEST/ffmpeg"
file "$DEST/ffmpeg" | cut -c1-80
"$DEST/ffmpeg" -version 2>/dev/null | head -1 || echo "(linux binary — version check skipped on this host)"
echo "OK: $DEST/ffmpeg"
