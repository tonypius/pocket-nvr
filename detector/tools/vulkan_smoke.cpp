// Phase 0 B smoke test (PLAN.md): burn FLAG-10/12 — run YOLO11n through
// NCNN on the Adreno 640 (Vulkan) with CPU fallback, on a real frame
// containing a person. Usage:
//   vulkan_smoke <model_dir> <frame.bgr> <w> <h> <vulkan|cpu> [repeats]
// repeats > 1 gives steady-state timing (first inference includes GPU
// shader compilation and is not representative).
#include "backend_ncnn.h"

#include <algorithm>
#include <cstdio>
#include <fstream>
#include <iterator>
#include <string>
#include <vector>

int main(int argc, char** argv) {
	if (argc < 6) {
		fprintf(stderr, "usage: %s <model_dir> <frame.bgr> <w> <h> <vulkan|cpu> [repeats]\n", argv[0]);
		return 2;
	}
	const std::string dir = argv[1], framePath = argv[2], want = argv[5];
	const int w = atoi(argv[3]), h = atoi(argv[4]);
	int repeats = argc > 6 ? atoi(argv[6]) : 1;
	if (repeats < 1) repeats = 1;

	std::ifstream f(framePath, std::ios::binary);
	if (!f) {
		fprintf(stderr, "cannot open frame %s\n", framePath.c_str());
		return 2;
	}
	nvr::Frame frame;
	frame.width = w;
	frame.height = h;
	frame.is_gray = false;
	frame.ts_ms = 0;
	frame.data.assign(std::istreambuf_iterator<char>(f), std::istreambuf_iterator<char>());
	if (frame.data.size() != size_t(w) * h * 3) {
		fprintf(stderr, "frame size mismatch: got %zu bytes, want %d\n",
				frame.data.size(), w * h * 3);
		return 2;
	}

	auto be = nvr::NcnnBackend::Open(dir, "yolo11n", 640, 0.25f, 0.45f, 0,
									 want == "vulkan");
	if (!be) {
		fprintf(stderr, "backend open failed\n");
		return 1;
	}
	printf("requested=%s backend=%s\n", want.c_str(), be->Name().c_str());

	int persons = 0;
	std::vector<nvr::Detection> dets;
	std::vector<double> runMs;
	for (int r = 0; r < repeats; ++r) {
		dets = be->Infer(frame);
		runMs.push_back(be->lastInferMs());
		if (r == 0) {
			printf("detections=%zu\n", dets.size());
			for (const auto& d : dets) {
				printf("  %s score=%.3f box=(%.0f,%.0f,%.0f,%.0f)\n",
					   d.label.c_str(), d.score, d.x, d.y, d.w, d.h);
				if (d.label == "person") ++persons;
			}
		}
	}
	// steady state = median of the last half (run 1 carries GPU shader
	// compilation / first-touch costs)
	std::sort(runMs.begin(), runMs.end());
	double median = 0;
	if (!runMs.empty()) {
		size_t h0 = runMs.size() / 2, h1 = runMs.size();
		median = runMs[h0 + (h1 - h0) / 2 - (h1 % 2 == 0 ? 1 : 0)];
		if (h1 - h0 == 0) median = runMs.back();
		median = runMs[(h0 + h1) / 2 == h1 ? h1 - 1 : (h0 + h1) / 2];
	}
	printf("detections_done=%d persons=%d\n", (int)dets.size(), persons);
	printf("avg_infer_ms=%.1f  steady_median_ms=%.1f  over %d runs\n",
		   be->avgInferMs(), median, repeats);

	if (persons == 0) {
		printf("SMOKE B: FAIL — no person detected\n");
		return 1;
	}
	printf("SMOKE B: PASS (backend=%s)\n", be->Name().c_str());
	return 0;
}
