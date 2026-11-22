// nvrdet — the PocketNVR detector daemon (COMP-3, FRS §4.3).
// Architecture (AD-1/AD-3): per-camera ffmpeg capture threads (motion-gated,
// sub-stream from MediaMTX localhost) → ONE shared bounded queue → ONE
// NCNN inference worker (CPU by default, Vulkan optional) → per-camera zones
// + event state machine →
// event POST to nvrd's internal API. A thermal governor scales the pipeline
// and a heartbeat thread reports gauges (FR-DET-12).
#include "backend_ncnn.h"
#include "capture.h"
#include "dconfig.h"
#include "event_state.h"
#include "frame_queue.h"
#include "governor.h"
#include "http_post.h"
#include "silence_feeder.h"
#include "snapshot.h"
#include "zones.h"

#include <algorithm>
#include <atomic>
#include <cmath>
#include <csignal>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <memory>
#include <string>
#include <thread>
#include <unistd.h>
#include <vector>

namespace {

std::atomic<bool> g_running{true};
std::atomic<double> g_fpsScale{1.0};
std::atomic<double> g_tempC{-1.0};
std::atomic<bool> g_reload{false}; // SIGHUP: re-exec once the pipeline drained

int64_t nowMs() {
	timespec ts{};
	clock_gettime(CLOCK_REALTIME, &ts);
	return int64_t(ts.tv_sec) * 1000 + ts.tv_nsec / 1000000;
}

std::string jsonEscape(const std::string& s) {
	std::string out;
	for (char c : s) {
		switch (c) {
		case '"': out += "\\\""; break;
		case '\\': out += "\\\\"; break;
		case '\n': out += "\\n"; break;
		default: out += c;
		}
	}
	return out;
}

int parseHostPort(const std::string& url, std::string& host, int& port) {
	// http://127.0.0.1:8099 → host/port
	size_t p = url.find("://");
	if (p == std::string::npos) return -1;
	size_t start = p + 3;
	size_t colon = url.find(':', start);
	size_t slash = url.find('/', start);
	if (colon == std::string::npos) return -1;
	host = url.substr(start, colon - start);
	int end = slash == std::string::npos ? int(url.size()) : int(slash);
	port = std::atoi(url.substr(colon + 1, end - colon - 1).c_str());
	return 0;
}

} // namespace

int main(int argc, char** argv) {
	// Control signals are consumed only by the sigwait thread below. They must
	// be blocked BEFORE any other thread starts so every thread inherits the
	// mask; blocking after the capture/worker threads exist left those threads
	// able to take SIGHUP. nvrctl launches us via nohup, which leaves SIGHUP
	// ignored (and the ignore survives exec), so a reload sent by nvrd was
	// silently discarded: restore the default disposition first.
	sigset_t set;
	sigemptyset(&set);
	sigaddset(&set, SIGHUP);
	sigaddset(&set, SIGTERM);
	sigaddset(&set, SIGINT);
	signal(SIGHUP, SIG_DFL);
	pthread_sigmask(SIG_BLOCK, &set, nullptr);

	const char* argv0 = argc > 0 ? argv[0] : "nvrdet";
	std::string cfgPath = "/data/nvr/detector.json";
	for (int i = 1; i < argc; ++i) {
		if (std::strcmp(argv[i], "-c") == 0 && i + 1 < argc) cfgPath = argv[++i];
		else if (std::strcmp(argv[i], "-v") == 0) { printf("nvrdet 0.1.0\n"); return 0; }
	}

	nvr::DetConfig cfg;
	try {
		cfg = nvr::loadConfig(cfgPath);
	} catch (const std::exception& e) {
		fprintf(stderr, "config error: %s\n", e.what());
		return 1;
	}
	fprintf(stderr, "nvrdet: %zu camera(s), backend=%s, %dx%d\n",
			cfg.cameras.size(), cfg.backend.c_str(), cfg.frame_w, cfg.frame_h);

	auto backend = nvr::NcnnBackend::Open(cfg.model_dir, cfg.model, cfg.input_size,
										  0.20f, 0.45f, 0, cfg.backend == "vulkan");
	if (!backend) {
		fprintf(stderr, "FATAL: model load failed (%s/%s)\n",
				cfg.model_dir.c_str(), cfg.model.c_str());
		return 1;
	}
	fprintf(stderr, "nvrdet: inference backend=%s\n", backend->Name().c_str());

	nvr::FrameQueue queue(size_t(cfg.queue_max));
	std::vector<std::unique_ptr<nvr::Capture>> captures;
	std::vector<nvr::EventStateMachine> machines;
	std::vector<nvr::CamCfg> cams;

	std::string api_host;
	int api_port = 8099;
	parseHostPort(cfg.api_url, api_host, api_port);

	// Per-camera "event open" flags: while a camera has an open event its
	// frames bypass the motion gate, so a person who stands still (and so
	// stops tripping the gate) keeps being analysed instead of being read as
	// "gone" by the silence feeder.
	std::unique_ptr<std::atomic<bool>[]> eventOpen(
		new std::atomic<bool>[cfg.cameras.empty() ? 1 : cfg.cameras.size()]);
	for (size_t i = 0; i < cfg.cameras.size(); ++i) eventOpen[i].store(false);

	for (size_t i = 0; i < cfg.cameras.size(); ++i) {
		const auto& c = cfg.cameras[i];
		machines.emplace_back(c.enter, c.exit_frames);
		cams.push_back(c);
		auto cap = std::make_unique<nvr::Capture>(c, int(cams.size() - 1),
												  cfg.frame_w, cfg.frame_h,
												  queue, g_fpsScale, &eventOpen[i]);
		cap->start();
		captures.push_back(std::move(cap));
	}

	// One inference worker (AD-3): frames → person filter → zones → state.
	std::thread worker([&] {
		// Per-camera peak tracking while an event is open.
		struct Peak {
			nvr::Frame frame;
			float bbox[4] = {0, 0, 0, 0};
			float score = 0;
			std::string label;
			bool set = false;
		};
		std::vector<Peak> peak(cams.size());
		std::vector<std::string> zoneHit(cams.size());
		nvr::SilenceFeeder silence(cams.size());

		auto emitEvent = [&](size_t slot) {
			auto& sm = machines[slot];
			const auto& cam = cams[slot];
			auto& pk = peak[slot];
			const nvr::EventRecord& rec = sm.completed();
			std::string body = "{";
			body += "\"camera_id\":\"" + jsonEscape(cam.id) + "\",";
			body += "\"label\":\"" + jsonEscape(pk.label.empty() ? "motion" : pk.label) + "\",";
			char num[128];
			snprintf(num, sizeof num, "%.3f", rec.peak_score);
			body += std::string("\"score\":") + num + ",";
			snprintf(num, sizeof num, "%lld", (long long)rec.start_ts);
			body += std::string("\"start_ts\":") + num + ",";
			snprintf(num, sizeof num, "%lld", (long long)rec.end_ts);
			body += std::string("\"end_ts\":") + num + ",";
			body += "\"zone\":" + (zoneHit[slot].empty()
									   ? std::string("null")
									   : "\"" + jsonEscape(zoneHit[slot]) + "\"") + ",";
			int64_t bb[4];
			nvr::bboxToArray(pk.bbox, bb);
			snprintf(num, sizeof num, "[%lld,%lld,%lld,%lld]", (long long)bb[0],
					 (long long)bb[1], (long long)bb[2], (long long)bb[3]);
			body += std::string("\"bbox\":") + num;
			if (pk.set) {
				auto jpeg = nvr::encodeSnapshotJpeg(pk.frame, pk.bbox);
				body += ",\"snapshot_jpeg_b64\":\"" +
						nvr::base64Encode(jpeg.data(), jpeg.size()) + "\"";
				auto cropJpeg = nvr::encodeCropJpeg(pk.frame, pk.bbox);
				if (!cropJpeg.empty())
					body += ",\"crop_jpeg_b64\":\"" +
							nvr::base64Encode(cropJpeg.data(), cropJpeg.size()) + "\"";
			}
			body += "}";
			// (a leftover debug dump wrote every event body, snapshots
			// included, to /data/local/tmp where adb shell can read it)
			bool ok = nvr::httpPost(api_host, api_port,
									"/api/internal/events", cfg.api_token, body);
			fprintf(stderr, "nvrdet: event on %s label=%s score=%.2f %s\n",
					cam.id.c_str(), pk.label.c_str(), rec.peak_score,
					ok ? "sent" : "SEND FAILED");
			pk.set = false;
			pk.score = 0;
			pk.frame.data.clear();
		};

		// Gate silence → implicit negatives (FR-DET-7): a still scene never
		// passes the motion gate, so no frames carry the "no person" samples
		// the state machine needs to close an event. Feed one negative per
		// camera frame interval while that camera is silent and its event is
		// open. Runs every iteration, clock-driven: with several cameras the
		// queue is rarely idle, so an idle-only feeder starved and events
		// never closed (soak-local-4cam-1h/NOTES.md).
		auto feedSilence = [&](int64_t now) {
			for (size_t i = 0; i < cams.size(); ++i) {
				if (!machines[i].active()) continue;
				if (!silence.Due(i, now, cams[i].fps)) continue;
				float zero[4] = {0, 0, 0, 0};
				if (machines[i].OnFrame(false, 0, zero, now) ==
					nvr::EventSignal::Exited)
					emitEvent(i);
				eventOpen[i].store(machines[i].active());
			}
		};

		while (g_running) {
			nvr::Frame f;
			// Short timeout bounds feeder latency when the queue is idle.
			bool got = queue.Pop(f, 50);
			if (got) silence.OnFrame(size_t(f.camera_slot), nowMs());
			feedSilence(nowMs());
			if (!got) continue;
			size_t slot = size_t(f.camera_slot);
			auto dets = backend->Infer(f);
			static std::atomic<uint64_t> dbgN{0};
			if (getenv("NVRDET_DEBUG") && dbgN.fetch_add(1) % 20 == 0) {
				fprintf(stderr, "dbg: slot=%zu %dx%d gray=%d dets=%zu", slot,
						f.width, f.height, int(f.is_gray), dets.size());
				for (auto& d : dets)
					fprintf(stderr, " [%s %.2f]", d.label.c_str(), d.score);
				fprintf(stderr, "\n");
			}
			const auto& cam = cams[slot];
			auto& sm = machines[slot];

			// best detection above this camera's threshold + inside zones;
			// label must be one of the camera's configured classes (B2)
			float best = 0;
			float bbox[4] = {0, 0, 0, 0};
			std::string zone;
			std::string bestLabel;
			nvr::Anchor anchor = cam.bottom_center ? nvr::Anchor::BottomCenter
												   : nvr::Anchor::Centroid;
			for (const auto& d : dets) {
				bool wanted = false;
				for (const auto& c : cam.classes)
					if (d.label == c) { wanted = true; break; }
				if (!wanted || d.score < cam.threshold || d.score <= best)
					continue;
				float b[4] = {d.x, d.y, d.w, d.h};
				if (!nvr::DetectionInZones(b, anchor, cam.zones)) continue;
				best = d.score;
				std::memcpy(bbox, b, sizeof b);
				bestLabel = d.label;
				zone = "";
				for (const auto& z : cam.zones) {
					float ax = b[0] + b[2] / 2;
					float ay = anchor == nvr::Anchor::BottomCenter ? b[1] + b[3]
																   : b[1] + b[3] / 2;
					if (nvr::PointInPolygon(ax, ay, z.polygon)) {
						zone = z.name;
						break;
					}
				}
			}
			bool present = best > 0;
			auto sig = sm.OnFrame(present, best, bbox, f.ts_ms);

			auto& pk = peak[slot];
			if (present && best > pk.score) {
				// remember the best frame for the snapshot (FR-DET-9)
				pk.frame = f;
				std::memcpy(pk.bbox, bbox, sizeof bbox);
				pk.score = best;
				pk.label = bestLabel;
				pk.set = true;
				zoneHit[slot] = zone;
			}

			if (sig == nvr::EventSignal::Exited) emitEvent(slot);
			eventOpen[slot].store(sm.active());
		}

		// Shutdown / reload: events still open would otherwise be lost (the
		// state machine only reports on exit). Close and report them now.
		for (size_t i = 0; i < cams.size(); ++i) {
			if (machines[i].ForceClose(nowMs())) emitEvent(i);
		}
	});

	nvr::Governor gov(cfg.temp_high, cfg.temp_low, g_fpsScale, g_tempC);
	gov.start();

	// Heartbeat (FR-DET-12): gauges → nvrd every 10 s.
	std::thread heartbeat([&] {
		std::string host = api_host;
		while (g_running) {
			for (int i = 0; i < 10 && g_running; ++i) usleep(1000 * 1000);
			if (!g_running) break;
			std::string body = "{\"backend\":\"" + backend->Name() + "\"";
			char num[128];
			snprintf(num, sizeof num, "%.1f", backend->avgInferMs());
			body += ",\"inference_ms\":" + std::string(num);
			body += ",\"queue_depth\":" + std::to_string(queue.size());
			snprintf(num, sizeof num, "%llu", (unsigned long long)queue.dropped());
			body += ",\"dropped_frames\":" + std::string(num);
			snprintf(num, sizeof num, "%.2f", g_fpsScale.load());
			body += ",\"fps_scale\":" + std::string(num);
			snprintf(num, sizeof num, "%.1f", g_tempC.load());
			body += ",\"temp_c\":" + std::string(num);
			body += ",\"cameras\":{";
			for (size_t i = 0; i < cams.size(); ++i) {
				if (i) body += ",";
				body += "\"" + jsonEscape(cams[i].id) + "\":{";
				snprintf(num, sizeof num, "%.2f", captures[i]->stats().fps.load());
				body += std::string("\"fps\":") + num + ",";
				snprintf(num, sizeof num, "%llu",
						 (unsigned long long)captures[i]->stats().gate_pass.load());
				body += std::string("\"gate_pass\":") + num + ",";
				snprintf(num, sizeof num, "%llu",
						 (unsigned long long)captures[i]->stats().gate_total.load());
				body += std::string("\"gate_total\":") + num + "}";

			}
			body += "}}";
			nvr::httpPost(host, api_port, "/api/internal/metrics", cfg.api_token, body);
		}
	});

	// SIGHUP / SIGTERM / SIGINT: stop the pipeline cleanly. The worker flushes
	// any open events on its way out; for SIGHUP the process re-execs itself
	// afterwards (FR-DET-11) so a reload never drops an in-flight event.
	// (signals are blocked at the top of main(), before any thread exists)
	std::thread signals([&] {
		int s = 0;
		sigwait(&set, &s);
		if (s == SIGHUP) {
			fprintf(stderr, "nvrdet: SIGHUP → re-exec\n");
			g_reload = true;
		} else {
			fprintf(stderr, "nvrdet: signal %d → shutdown\n", s);
		}
		g_running = false;
		queue.Stop();
	});

	worker.join();
	for (auto& c : captures) c->stop();
	gov.stop();
	signals.join();
	heartbeat.join(); // a joinable std::thread at scope exit aborts the process

	if (g_reload) {
		// exec runs no destructors; captures (and their ffmpeg children) were
		// stopped above or each reload would leak one ffmpeg per camera.
		// Keep the ORIGINAL argv[0]: nvrctl identifies the daemon by it, and a
		// bare "nvrdet" made the watchdog start a second detector.
		execl("/proc/self/exe", argv0, "-c", cfgPath.c_str(), (char*)nullptr);
		// non-Android fallback
		execlp(argv0, argv0, "-c", cfgPath.c_str(), (char*)nullptr);
		_exit(127);
	}
	fprintf(stderr, "nvrdet: stopped\n");
	return 0;
}
