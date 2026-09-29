#include "frame_queue.h"

#include <chrono>

namespace nvr {

bool FrameQueue::Push(Frame f) {
	pushed_.fetch_add(1, std::memory_order_relaxed);
	{
		std::lock_guard<std::mutex> lk(m_);
		if (stopped_) return false;
		if (q_.size() >= max_) {
			q_.pop_front(); // drop oldest — freshness beats completeness
			dropped_.fetch_add(1, std::memory_order_relaxed);
		}
		q_.push_back(std::move(f));
	}
	cv_.notify_one();
	return true;
}

bool FrameQueue::Pop(Frame& out, int timeout_ms) {
	std::unique_lock<std::mutex> lk(m_);
	if (!cv_.wait_for(lk, std::chrono::milliseconds(timeout_ms), [&] {
			return stopped_ || !q_.empty();
		})) {
		return false;
	}
	if (q_.empty()) return false; // stopped
	out = std::move(q_.front());
	q_.pop_front();
	return true;
}

void FrameQueue::Stop() {
	{
		std::lock_guard<std::mutex> lk(m_);
		stopped_ = true;
	}
	cv_.notify_all();
}

size_t FrameQueue::size() const {
	std::lock_guard<std::mutex> lk(m_);
	return q_.size();
}

bool FrameQueue::isStopped() const {
	std::lock_guard<std::mutex> lk(m_);
	return stopped_;
}

} // namespace nvr
