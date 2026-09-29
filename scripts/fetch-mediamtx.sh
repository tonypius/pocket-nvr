#!/usr/bin/env bash
# Fetch MediaMTX release binaries: host (macOS arm64, dev testing) and
# linux/arm64 (the phone). Pinned under third_party/mediamtx/<version>/.
# Usage: scripts/fetch-mediamtx.sh [version]   (default: latest release)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/third_party/mediamtx"

api=https://api.github.com/repos/bluenviron/mediamtx/releases
if [[ $# -ge 1 ]]; then
  meta=$(curl -sfL "$api/tags/$1")
else
  meta=$(curl -sfL "$api/latest")
fi
ver=$(printf '%s' "$meta" | MEDIA_META="$meta" python3 -c 'import json,os; print(json.loads(os.environ["MEDIA_META"])["tag_name"])')
echo "MediaMTX version: $ver"

# Per-platform subdirs: one shared filename across tarballs would silently
# overwrite the other platform's binary.
for pair in linux_arm64:linux_arm64 darwin_arm64:darwin_arm64; do
  plat="${pair%%:*}"
  outdir="$DEST/$ver/$plat"
  mkdir -p "$outdir"
  url=$(MEDIA_META="$meta" MEDIA_PLAT="$plat" python3 -c '
import json, os
assets = json.loads(os.environ["MEDIA_META"])["assets"]
plat = os.environ["MEDIA_PLAT"]
matches = [a for a in assets if plat in a["name"] and a["name"].endswith(".tar.gz")]
print(matches[0]["browser_download_url"] if matches else "")
') || true
  if [[ -z "$url" ]]; then
    echo "ERROR: no asset matching '$plat' for $ver" >&2
    exit 1
  fi
  echo "fetching $(basename "$url")"
  curl -sfL -o "$outdir/mediamtx.tar.gz" "$url"
  tar -xzf "$outdir/mediamtx.tar.gz" -C "$outdir" mediamtx
  rm "$outdir/mediamtx.tar.gz"
done
chmod +x "$DEST/$ver"/*/mediamtx
ln -sfn "$ver" "$DEST/current"
"$DEST/current/darwin_arm64/mediamtx" --version
echo "OK: $DEST/current/{linux_arm64,darwin_arm64}/mediamtx"
