// Snapshot rendering for events (FR-DET-9): draws the peak bbox on the
// frame and encodes JPEG in-memory via vendored stb_image_write.
#pragma once

#include "types.h"

#include <cstdint>
#include <vector>

namespace nvr {

// Returns JPEG bytes of the frame with a green box at bbox (frame coords).
std::vector<uint8_t> encodeSnapshotJpeg(const Frame& f, const float bbox[4]);

// Returns JPEG bytes of the cropped detection region (bbox + 12% padding),
// scaled so the long edge is ~320px — the "object thumbnail" for the
// Detections grid and Home strip (UI-PLAN B1).
std::vector<uint8_t> encodeCropJpeg(const Frame& f, const float bbox[4]);

} // namespace nvr
