// Capture (FR-DET-1): one thread per camera spawning a static ffmpeg that
// decodes the sub-stream (from MediaMTX localhost restream, AD-1) into raw
// BGR frames at the configured fps. Frames pass the motion gate
// (FR-DET-2) before entering the shared queue; the pipe is restarted with
// backoff on EOF/error (FLAG-9 resilience).
#pragma once

#include "dconfig.h"
#include "frame_queue.h"
#include "motion_gate.h"
#include "types.h"

#include <atomic>
#include <cstdint>
#include <memory>
#include <thread>

namespace nvr {

struct CamStats {
	std::atomic<uint64_t> gate_total{0};
	std::atomic<uint64_t> gate_pass{0};
	std::atomic<double> fps{0}; // effective forwarded fps (FR-DET-12)
};

class Capture {
public:
	Capture(CamCfg cfg, int slot, int frame_w, int frame_h,
			FrameQueue& queue, const std::atomic<double>& fpsScale);
	~Capture() { stop(); }

	void start();
	void stop();

	const CamStats& stats() const { return stats_; }

private:
	void run();
	bool pump(int fd); // read frames from fd until error; false = restart

	CamCfg cfg_;
	int slot_, w_, h_;
	FrameQueue& queue_;
	const std::atomic<double>& fps_scale_;
	MotionGate gate_;
	CamStats stats_;
	std::thread th_;
	std::atomic<bool> stopping_{false};
	int64_t last_push_ms_ = 0;
};

} // namespace nvr
