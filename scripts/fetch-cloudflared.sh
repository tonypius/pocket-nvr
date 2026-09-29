#!/usr/bin/env bash
# Fetch cloudflared (Cloudflare Tunnel client): host (macOS arm64, dev
# testing) and linux/arm64 (the phone — Design B edge, deploy/cloudflare/).
# Pinned under third_party/cloudflared/<version>/.
# Usage: scripts/fetch-cloudflared.sh [version]   (default: latest release)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/third_party/cloudflared"

api=https://api.github.com/repos/cloudflare/cloudflared/releases
if [[ $# -ge 1 ]]; then
  meta=$(curl -sfL "$api/tags/$1")
else
  meta=$(curl -sfL "$api/latest")
fi
ver=$(printf '%s' "$meta" | CFL_META="$meta" python3 -c 'import json,os; print(json.loads(os.environ["CFL_META"])["tag_name"])')
echo "cloudflared version: $ver"

# linux/arm64 is a bare binary; darwin/arm64 ships as a .tar.gz.
outdir="$DEST/$ver/linux_arm64"
mkdir -p "$outdir"
url=$(CFL_META="$meta" python3 -c '
import json, os
assets = json.loads(os.environ["CFL_META"])["assets"]
m = [a for a in assets if a["name"] == "cloudflared-linux-arm64"]
print(m[0]["browser_download_url"] if m else "")
')
[[ -n "$url" ]] || { echo "ERROR: no cloudflared-linux-arm64 asset for $ver" >&2; exit 1; }
echo "fetching $(basename "$url")"
curl -sfL -o "$outdir/cloudflared" "$url"
chmod +x "$outdir/cloudflared"

outdir="$DEST/$ver/darwin_arm64"
mkdir -p "$outdir"
url=$(CFL_META="$meta" python3 -c '
import json, os
assets = json.loads(os.environ["CFL_META"])["assets"]
m = [a for a in assets if a["name"] == "cloudflared-darwin-arm64.tgz"]
print(m[0]["browser_download_url"] if m else "")
')
if [[ -n "$url" ]]; then
  echo "fetching $(basename "$url")"
  curl -sfL -o "$outdir/cloudflared.tgz" "$url"
  tar -xzf "$outdir/cloudflared.tgz" -C "$outdir" cloudflared
  rm "$outdir/cloudflared.tgz"
  chmod +x "$outdir/cloudflared"
else
  echo "(no cloudflared-darwin-arm64.tgz asset — skipping host copy)"
fi

ln -sfn "$ver" "$DEST/current"
# version check with whatever runs on this host — the linux/arm64 binary
# is staged for the phone and can't execute on the Mac
"$DEST/current/darwin_arm64/cloudflared" --version 2>/dev/null \
  || echo "(linux/arm64 cloudflared staged for the phone; not executable on this host)"
echo "OK: $DEST/current/{linux_arm64,darwin_arm64}/cloudflared"
