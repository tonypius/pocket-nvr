// Thermal governor (FR-DET-10, NFR-5): reads the SoC temperature and
// drives a global fps scale — 1.0 normal, 0.5 stepped down above
// temp_high, 0 (paused) well above it, resuming below temp_low with
// hysteresis. On the phone the zone read is the SoC; off-Linux it is a
// no-op that stays at 1.0.
#pragma once

#include <atomic>
#include <string>
#include <thread>

namespace nvr {

class Governor {
public:
	Governor(int temp_high_c, int temp_low_c, std::atomic<double>& fpsScale,
			 std::atomic<double>& lastTempC)
		: high_(temp_high_c), low_(temp_low_c), scale_(fpsScale), temp_(lastTempC) {}

	void start() { th_ = std::thread([this] { run(); }); }
	void stop() {
		stopping_ = true;
		if (th_.joinable()) th_.join();
	}

private:
	void run();

	int high_, low_;
	std::atomic<double>& scale_;
	std::atomic<double>& temp_;
	std::thread th_;
	std::atomic<bool> stopping_{false};
};

} // namespace nvr
