#include "motion_gate.h"

namespace nvr {

namespace {
// Downscale target: ~96 columns keeps the diff cost trivial on any CPU
// while preserving enough spatial detail for area estimation.
constexpr int kTargetCols = 96;

int clampi(int v, int lo, int hi) { return v < lo ? lo : (v > hi ? hi : v); }
} // namespace

bool MotionGate::Process(const uint8_t* gray, int width, int height) {
	if (width <= 0 || height <= 0) return false;

	// nearest-neighbor downscale to kTargetCols wide
	int cols = width < kTargetCols ? width : kTargetCols;
	int rows = (height * cols + width / 2) / width;
	if (rows < 1) rows = 1;
	if (cols < 1) cols = 1;

	std::vector<uint8_t> cur(static_cast<size_t>(cols) * rows);
	for (int r = 0; r < rows; ++r) {
		int sy = (r * height + rows / 2) / rows;
		sy = clampi(sy, 0, height - 1);
		for (int c = 0; c < cols; ++c) {
			int sx = (c * width + cols / 2) / cols;
			sx = clampi(sx, 0, width - 1);
			cur[static_cast<size_t>(r) * cols + c] = gray[static_cast<size_t>(sy) * width + sx];
		}
	}

	if (prev_.size() != cur.size()) {
		// first frame or geometry change: seed, no motion possible
		prev_ = std::move(cur);
		last_area_ = 0;
		return false;
	}

	size_t changed = 0;
	for (size_t i = 0; i < cur.size(); ++i) {
		int d = cur[i] - prev_[i];
		if (d < 0) d = -d;
		if (d > diff_thresh_) ++changed;
	}
	const float denom = static_cast<float>(cur.size());
	last_area_ = denom > 0 ? static_cast<float>(changed) / denom : 0.0f;
	prev_ = std::move(cur);

	return last_area_ >= min_area_;
}

} // namespace nvr
