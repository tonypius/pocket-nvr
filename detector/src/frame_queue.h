// Bounded frame queue (FR-DET-5, AD-3): many camera capture threads feed
// ONE inference worker. On overflow the OLDEST frame is dropped so the
// worker always sees the freshest scene and memory stays bounded.
// Drop accounting feeds the FR-DET-12 metrics.
#pragma once

#include "types.h"

#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <mutex>
#include <utility>

namespace nvr {

class FrameQueue {
public:
	explicit FrameQueue(size_t max_size) : max_(max_size) {}

	// Producer: never blocks. Drops oldest when full. Returns false if this
	// frame was the one dropped (queue full and this frame arrived last).
	bool Push(Frame f);

	// Consumer: blocks up to timeout_ms. Returns false on timeout/stop.
	bool Pop(Frame& out, int timeout_ms);

	// Wake all blocked Pop callers; further Pops time out immediately.
	void Stop();

	size_t size() const;
	bool isStopped() const;

	uint64_t dropped() const { return dropped_.load(std::memory_order_relaxed); }
	uint64_t pushed() const { return pushed_.load(std::memory_order_relaxed); }

private:
	const size_t max_;
	std::deque<Frame> q_;
	mutable std::mutex m_;
	std::condition_variable cv_;
	bool stopped_ = false;
	std::atomic<uint64_t> dropped_{0};
	std::atomic<uint64_t> pushed_{0};
};

} // namespace nvr
