#include "event_state.h"

namespace nvr {

EventSignal EventStateMachine::OnFrame(bool present, float best_score,
									   const float best_bbox[4],
									   int64_t now_ms) {
	if (present) {
		neg_run_ = 0;
		++pos_run_;
	} else {
		pos_run_ = 0;
		if (active_) {
			++neg_run_;
		}
	}

	if (!active_) {
		if (pos_run_ >= enter_frames_) {
			active_ = true;
			// Event starts at the first positive frame of the run.
			start_ts_ = first_pos_ts_ ? first_pos_ts_ : now_ms;
			peak_score_ = best_score;
			for (int i = 0; i < 4; ++i) peak_bbox_[i] = best_bbox[i];
			return EventSignal::Entered;
		}
		if (present) {
			if (first_pos_ts_ == 0) first_pos_ts_ = now_ms;
		} else {
			first_pos_ts_ = 0; // run aborted before entering
		}
		return EventSignal::None;
	}

	// active
	if (present) {
		if (best_score > peak_score_) {
			peak_score_ = best_score;
			for (int i = 0; i < 4; ++i) peak_bbox_[i] = best_bbox[i];
		}
		return EventSignal::Active;
	}

	if (neg_run_ >= exit_frames_) {
		completed_.start_ts = start_ts_;
		completed_.end_ts = now_ms;
		completed_.peak_score = peak_score_;
		for (int i = 0; i < 4; ++i) completed_.peak_bbox[i] = peak_bbox_[i];
		active_ = false;
		neg_run_ = 0;
		first_pos_ts_ = 0;
		return EventSignal::Exited;
	}
	return EventSignal::Active;
}

bool EventStateMachine::ForceClose(int64_t now_ms) {
	if (!active_) return false;
	completed_.start_ts = start_ts_;
	completed_.end_ts = now_ms;
	completed_.peak_score = peak_score_;
	for (int i = 0; i < 4; ++i) completed_.peak_bbox[i] = peak_bbox_[i];
	active_ = false;
	neg_run_ = 0;
	pos_run_ = 0;
	first_pos_ts_ = 0;
	return true;
}

} // namespace nvr
