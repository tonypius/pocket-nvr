// Core types for the PocketNVR detector (COMP-3). Portable C++17: builds
// on macOS (unit tests) and Android NDK/bionic (phone) unchanged.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace nvr {

// A decoded video frame. data is interleaved BGR (3 bytes/px) for inference
// and gray for the gate — produced by the ffmpeg capture pipe.
struct Frame {
	std::vector<uint8_t> data;
	int width = 0;
	int height = 0;
	bool is_gray = false;
	int64_t ts_ms = 0;   // capture timestamp (epoch ms, FLAG-14)
	int camera_slot = 0; // index into detector's camera table
};

// One model output box after class filtering (FR-DET-3: person only).
struct Detection {
	float x = 0, y = 0, w = 0, h = 0; // pixels in frame space
	float score = 0;
	std::string label;
};

// Completed event handed to COMP-4 (FRS §5.2 shape).
struct EventRecord {
	std::string camera_id;
	int64_t start_ts = 0;
	int64_t end_ts = 0;
	float peak_score = 0;
	float peak_bbox[4] = {0, 0, 0, 0}; // x,y,w,h at peak frame
	std::string zone;                  // zone name or empty
	std::vector<uint8_t> snapshot_jpeg; // annotated trigger frame (FR-DET-9)
};

// Polygon zone: name + points in frame pixel coordinates (FRS §6.3).
struct Zone {
	std::string name;
	std::vector<std::pair<float, float>> polygon;
};

// [x,y,w,h] as int64 for JSON output.
inline void bboxToArray(const float b[4], int64_t out[4]) {
	for (int i = 0; i < 4; ++i) out[i] = static_cast<int64_t>(b[i] + 0.5f);
}

} // namespace nvr
