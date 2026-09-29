// Motion gate (FR-DET-2): grayscale downscale + frame diff. A frame is
// forwarded to inference only when the changed-pixel area exceeds the
// per-camera threshold (fraction of frame). Static scene → ~0 inference.
#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace nvr {

class MotionGate {
public:
	// min_area: changed fraction of frame (0..1) needed to trigger.
	// diff_thresh: per-pixel gray delta counting as "changed" (0..255).
	MotionGate(float min_area, uint8_t diff_thresh = 15)
		: min_area_(min_area), diff_thresh_(diff_thresh) {}

	// Feed one grayscale frame. Returns true when motion area >= min_area.
	// Downscales internally to ~96-wide rows so cost is negligible.
	bool Process(const uint8_t* gray, int width, int height);

	// Fraction of changed pixels observed in the last Process call.
	float lastChangedArea() const { return last_area_; }

	// Drop history (e.g. after camera reconnect or fps change).
	void Reset() { prev_.clear(); last_area_ = 0; }

private:
	float min_area_;
	uint8_t diff_thresh_;
	std::vector<uint8_t> prev_; // downscaled previous frame
	float last_area_ = 0;
};

} // namespace nvr
