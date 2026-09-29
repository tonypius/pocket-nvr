#!/usr/bin/env bash
# Build the C++ detector (nvrdet) for the phone: NCNN (Vulkan on) + detector,
# both cross-compiled with the Android NDK for arm64-v8a.
# Output: detector/build-android-ncnn/nvrdet   (picked up by scripts/deploy.sh)
# Usage: ANDROID_NDK=/path/to/android-ndk-r27c scripts/build_detector_android.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NCNN_TAG="${NCNN_TAG:-20260526}"
NCNN_SRC="$ROOT/third_party/ncnn-src"
NCNN_BUILD="$ROOT/third_party/ncnn-android-arm64"
NCNN_INSTALL="$ROOT/third_party/ncnn-android-arm64-install"

NDK="${ANDROID_NDK:-${ANDROID_NDK_HOME:-}}"
[ -n "$NDK" ] && [ -f "$NDK/build/cmake/android.toolchain.cmake" ] || {
  echo "set ANDROID_NDK to an Android NDK (r27+) directory"; exit 1; }
TOOLCHAIN="$NDK/build/cmake/android.toolchain.cmake"
JOBS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu)"

if [ ! -d "$NCNN_SRC" ]; then
  echo "== cloning ncnn $NCNN_TAG"
  git clone --depth 1 --branch "$NCNN_TAG" https://github.com/Tencent/ncnn "$NCNN_SRC"
  git -C "$NCNN_SRC" submodule update --init --depth 1
fi

echo "== building ncnn (android arm64, vulkan)"
cmake -S "$NCNN_SRC" -B "$NCNN_BUILD" \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DANDROID_ABI=arm64-v8a -DANDROID_PLATFORM=android-28 \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$NCNN_INSTALL" \
  -DNCNN_VULKAN=ON -DNCNN_OPENMP=OFF -DNCNN_DISABLE_RTTI=ON -DNCNN_SIMPLEOCV=OFF \
  -DNCNN_BUILD_TOOLS=OFF -DNCNN_BUILD_EXAMPLES=OFF -DNCNN_BUILD_TESTS=OFF -DNCNN_BUILD_BENCHMARK=ON
cmake --build "$NCNN_BUILD" -j "$JOBS"
cmake --install "$NCNN_BUILD"

echo "== building nvrdet"
cmake -S "$ROOT/detector" -B "$ROOT/detector/build-android-ncnn" \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DANDROID_ABI=arm64-v8a -DANDROID_PLATFORM=android-28 \
  -DCMAKE_BUILD_TYPE=Release -Dncnn_DIR="$NCNN_INSTALL/lib/cmake/ncnn"
cmake --build "$ROOT/detector/build-android-ncnn" --target nvrdet -j "$JOBS"
file "$ROOT/detector/build-android-ncnn/nvrdet" | cut -c1-120
echo "OK: detector/build-android-ncnn/nvrdet"
