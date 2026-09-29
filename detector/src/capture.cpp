#include "capture.h"

#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <sys/wait.h>
#include <unistd.h>

#include <cmath>
#include <cstring>
#include <utility>

namespace nvr {

namespace {

int64_t nowMs() {
	timespec ts{};
	clock_gettime(CLOCK_REALTIME, &ts);
	return int64_t(ts.tv_sec) * 1000 + ts.tv_nsec / 1000000;
}

} // namespace

Capture::Capture(CamCfg cfg, int slot, int frame_w, int frame_h,
				 FrameQueue& queue, const std::atomic<double>& fpsScale)
	: cfg_(std::move(cfg)), slot_(slot), w_(frame_w), h_(frame_h),
	  queue_(queue), fps_scale_(fpsScale), gate_(cfg.motion_area) {}

void Capture::start() {
	th_ = std::thread([this] { run(); });
}

void Capture::stop() {
	stopping_ = true;
	if (th_.joinable()) th_.join();
}

void Capture::run() {
	while (!stopping_) {
		// Scale to the fixed analyze frame and cap fps in ffmpeg so decode
		// cost stays at the configured cadence (FR-DET-1). Absolute fallback
		// keeps capture working even when PATH lacks our bin dir.
		std::string cmd =
			"FF=$(command -v ffmpeg 2>/dev/null || echo /data/nvr/bin/ffmpeg); "
			"$FF -loglevel error -nostdin -rtsp_transport tcp -i " + cfg_.sub_url +
			" -vf fps=" + std::to_string(cfg_.fps) +
			",scale=" + std::to_string(w_) + ":" + std::to_string(h_) +
			" -pix_fmt bgr24 -f rawvideo pipe:1";

		int pfd[2];
		if (pipe(pfd) != 0) { sleep(2); continue; }
		pid_t pid = fork();
		if (pid < 0) { close(pfd[0]); close(pfd[1]); sleep(2); continue; }
		if (pid == 0) {
			// child: ffmpeg writes to the pipe
			dup2(pfd[1], STDOUT_FILENO);
			close(pfd[0]);
			close(pfd[1]);
			execl("/system/bin/sh", "sh", "-c", cmd.c_str(), (char*)nullptr);
			// /system/bin/sh exists on Android; fall back to plain exec for
			// desktop builds where sh is elsewhere.
			execlp("sh", "sh", "-c", cmd.c_str(), (char*)nullptr);
			_exit(127);
		}
		close(pfd[1]);
		fcntl(pfd[0], F_SETFL, O_NONBLOCK);

		bool ok = pump(pfd[0]);
		kill(pid, SIGKILL);
		waitpid(pid, nullptr, 0);
		close(pfd[0]);
		gate_.Reset(); // avoid fake motion across the reconnect gap
		if (!stopping_) sleep(ok ? 0 : 2); // backoff on error (FLAG-9)
	}
}

bool Capture::pump(int fd) {
	const size_t frame_bytes = size_t(w_) * h_ * 3;
	std::vector<uint8_t> buf(frame_bytes);
	std::vector<uint8_t> gray(size_t(w_) * h_);
	size_t have = 0;
	uint64_t skip_phase = 0;
	int64_t window_start = nowMs();
	uint64_t window_frames = 0;

	while (!stopping_) {
		pollfd p{fd, POLLIN, 0};
		if (poll(&p, 1, 500) <= 0) {
			if (p.revents & (POLLERR | POLLHUP)) return false;
			continue; // quiet timeout — check stopping flag
		}
		ssize_t n = read(fd, buf.data() + have, frame_bytes - have);
		if (n <= 0) return false; // EOF/error → restart with backoff
		have += size_t(n);
		if (have < frame_bytes) continue;
		have = 0;

		// grayscale copy for the gate (FR-DET-2)
		for (size_t i = 0; i < gray.size(); ++i) {
			size_t j = i * 3;
			gray[i] = uint8_t((buf[j] * 299 + buf[j + 1] * 587 + buf[j + 2] * 114) / 1000);
		}
		stats_.gate_total.fetch_add(1);
		bool motion = gate_.Process(gray.data(), w_, h_);
		if (!motion) continue;

		// Thermal governor (FR-DET-10): 1.0 = normal, 0.5 = every other
		// frame, 0 = paused.
		double scale = fps_scale_.load();
		if (scale <= 0) continue;
		if (scale < 1.0 && (++skip_phase & 1)) continue;

		Frame f;
		f.width = w_;
		f.height = h_;
		f.is_gray = false;
		f.ts_ms = nowMs();
		f.camera_slot = slot_;
		f.data = buf; // forward the BGR frame
		queue_.Push(std::move(f));
		stats_.gate_pass.fetch_add(1);

		// effective fps over a 5 s window
		++window_frames;
		int64_t dt = f.ts_ms - window_start;
		if (dt >= 5000) {
			stats_.fps.store(double(window_frames) * 1000.0 / double(dt));
			window_start = f.ts_ms;
			window_frames = 0;
		}
	}
	return true;
}

} // namespace nvr
