// Event state machine (FR-DET-7): per camera. Enter after `enter_frames`
// consecutive positives, exit after `exit_frames` consecutive negatives.
// One event spans enter→exit with peak score/bbox tracking. Brief flicker
// below enter_frames yields nothing.
#pragma once

#include "types.h"

#include <cstdint>

namespace nvr {

enum class EventSignal {
	None,     // no state change worth reporting
	Active,   // event ongoing (already entered)
	Entered,  // event started this frame
	Exited,   // event completed — caller should emit EventRecord
};

class EventStateMachine {
public:
	EventStateMachine(int enter_frames, int exit_frames)
		: enter_frames_(enter_frames < 1 ? 1 : enter_frames),
		  exit_frames_(exit_frames < 1 ? 1 : exit_frames) {}

	// Feed one per-frame detection result (after zones filter).
	// present = any person detection inside zones this frame.
	EventSignal OnFrame(bool present, float best_score, const float best_bbox[4],
						int64_t now_ms);

	// Snapshot of the completed event after Exited; start/peak are filled.
	const EventRecord& completed() const { return completed_; }

	bool active() const { return active_; }
	int64_t startTs() const { return start_ts_; }
	float peakScore() const { return peak_score_; }

	// Force-close an open event (camera disable / shutdown). Returns true
	// if there was an open event to close.
	bool ForceClose(int64_t now_ms);

private:
	int enter_frames_;
	int exit_frames_;
	int pos_run_ = 0;
	int neg_run_ = 0;
	bool active_ = false;
	int64_t start_ts_ = 0;
	int64_t first_pos_ts_ = 0;
	float peak_score_ = 0;
	float peak_bbox_[4] = {0, 0, 0, 0};
	EventRecord completed_;
};

} // namespace nvr
