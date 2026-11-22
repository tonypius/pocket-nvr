// Implicit-negative scheduling for motion-gated cameras (FR-DET-7).
//
// The motion gate drops frames from a still scene, so once a person leaves,
// an open event receives no "not present" samples and would never close. The
// worker therefore feeds the state machine one synthetic negative per camera
// frame interval while that camera is silent.
//
// This must be driven by the clock on every worker iteration, NOT only when
// the shared queue is idle: with several cameras the queue is almost never
// empty for a whole Pop timeout, so an idle-only feeder starves and open
// events never close (bench-results/soak-local-4cam-1h/NOTES.md).
#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <vector>

namespace nvr {

class SilenceFeeder {
public:
	explicit SilenceFeeder(size_t cameras)
		: last_frame_(cameras, 0), last_sample_(cameras, 0) {}

	// A real frame from `slot` reached the worker: the camera is not silent.
	void OnFrame(size_t slot, int64_t ts_ms) {
		last_frame_[slot] = ts_ms;
		last_sample_[slot] = ts_ms;
	}

	// True when `slot` has produced no frame for longer than its frame
	// interval and no negative was fed within that interval. Records the
	// sample, so call it only for cameras with an open event.
	bool Due(size_t slot, int64_t now_ms, int fps) {
		int64_t interval = 1000 / std::max(1, fps);
		if (now_ms - last_frame_[slot] <= interval) return false;
		if (now_ms - last_sample_[slot] < interval) return false;
		last_sample_[slot] = now_ms;
		return true;
	}

private:
	std::vector<int64_t> last_frame_;
	std::vector<int64_t> last_sample_;
};

} // namespace nvr
