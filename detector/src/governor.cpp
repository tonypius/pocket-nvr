#include "governor.h"

#include <cstdio>
#include <ctime>

namespace nvr {

namespace {

int64_t nowMs() {
	timespec ts{};
	clock_gettime(CLOCK_REALTIME, &ts);
	return int64_t(ts.tv_sec) * 1000 + ts.tv_nsec / 1000000;
}

// Millidegrees C from the first thermal zone; -1 when unavailable.
double readSoCTemp() {
	FILE* f = fopen("/sys/class/thermal/thermal_zone0/temp", "r");
	if (!f) return -1.0;
	int milli = 0;
	int n = fscanf(f, "%d", &milli);
	fclose(f);
	if (n != 1 || milli <= 0) return -1.0;
	return milli / 1000.0;
}

} // namespace

void Governor::run() {
	(void)nowMs();
	enum class State { Normal, Stepped, Paused };
	State st = State::Normal;
	scale_.store(1.0);

	while (!stopping_) {
		double t = readSoCTemp();
		if (t > 0) temp_.store(t);

		switch (st) {
		case State::Normal:
			if (t > 0 && t >= high_ + 10.0) {
				st = State::Paused;
				scale_.store(0.0);
			} else if (t > 0 && t >= high_) {
				st = State::Stepped;
				scale_.store(0.5);
			}
			break;
		case State::Stepped:
			if (t > 0 && t >= high_ + 10.0) {
				st = State::Paused;
				scale_.store(0.0);
			} else if (t > 0 && t <= low_) {
				st = State::Normal;
				scale_.store(1.0);
			}
			break;
		case State::Paused:
			if (t > 0 && t <= low_) {
				st = State::Stepped;
				scale_.store(0.5);
			}
			break;
		}
		timespec ts{5, 0}; // 5 s cadence — thermal mass is slow
		nanosleep(&ts, nullptr);
	}
}

} // namespace nvr
