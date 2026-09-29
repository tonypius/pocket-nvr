#!/usr/bin/env bash
# Fetch YOLO11n and export to NCNN (models/yolo11n/{yolo11n.param,yolo11n.bin}).
# Uses a local venv (models/.venv, gitignored) with ultralytics + ncnn.
# License note (FLAG-11): YOLO11n weights are AGPL-3.0 — fine for personal
# use; not committed to the repo — each user generates them locally.
# Usage: scripts/fetch_model.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODELS="$ROOT/models"
VENV="$MODELS/.venv"
mkdir -p "$MODELS"

# ultralytics/torch wheels lag on brand-new CPython; prefer a pinned python.
PY=""
for cand in python3.12 python3.11 python3; do
  if command -v "$cand" >/dev/null; then PY="$cand"; break; fi
done
echo "python: $PY ($($PY --version 2>&1))"

[ -x "$VENV/bin/pip" ] || "$PY" -m venv "$VENV"
"$VENV/bin/pip" install -q --upgrade pip
"$VENV/bin/pip" install -q "ultralytics" "ncnn"
"$VENV/bin/python" - <<'PY'
import ultralytics, sys
print("ultralytics", ultralytics.__version__, "on", sys.version)
PY

cd "$MODELS"
"$VENV/bin/yolo" export model=yolo11n.pt format=ncnn imgsz=640

SRC="$MODELS/yolo11n_ncnn_model"
mkdir -p "$MODELS/yolo11n"
cp "$SRC"/*.param "$MODELS/yolo11n/yolo11n.param"
cp "$SRC"/*.bin "$MODELS/yolo11n/yolo11n.bin"
ls -lh "$MODELS/yolo11n/"

# Inspect the export: blob names + output tensor shape must match what
# detector/src/backend_ncnn.cpp decodes (in0 → out0, (4+nc) x N).
"$VENV/bin/python" - "$MODELS/yolo11n" <<'PY'
import sys, ncnn
import numpy as np
d = sys.argv[1]
net = ncnn.Net()
net.opt.use_vulkan_compute = False
if net.load_param(d + "/yolo11n.param") != 0 or net.load_model(d + "/yolo11n.bin") != 0:
    sys.exit("model load failed")
ins, outs = net.input_names(), net.output_names()
print("INPUT/OUTPUT blobs:", ins, outs)
img = np.random.randint(0, 255, (640, 640, 3), dtype=np.uint8)
mat = ncnn.Mat.from_pixels(img, ncnn.Mat.PixelType.PIXEL_BGR2RGB, 640, 640)
mat.substract_mean_normalize([0.0, 0.0, 0.0], [1/255.0, 1/255.0, 1/255.0])
ex = net.create_extractor()
ex.input(ins[0], mat)
code, out = ex.extract(outs[0])
print("extract rc:", code, "dims:", out.dims, "c:", out.c, "h:", out.h, "w:", out.w)
assert (out.dims, out.h, out.w) == (2, 84, 8400), "unexpected output shape"
print("shape OK: (4+80 classes) x 8400 candidates")
PY
echo "OK: model exported + inspected"
