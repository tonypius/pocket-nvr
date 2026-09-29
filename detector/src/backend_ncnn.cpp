#include "backend_ncnn.h"

#include "coco.h"
#include "net.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <mutex>

#ifdef NCNN_VULKAN
#include "gpu.h"
#endif

namespace nvr {

namespace {

struct BoxScore {
	float x, y, w, h, score;
	int cls = 0;
};

float IoU(const BoxScore& a, const BoxScore& b) {
	float x1 = std::max(a.x, b.x), y1 = std::max(a.y, b.y);
	float x2 = std::min(a.x + a.w, b.x + b.w);
	float y2 = std::min(a.y + a.h, b.y + b.h);
	float inter = std::max(0.f, x2 - x1) * std::max(0.f, y2 - y1);
	float uni = a.w * a.h + b.w * b.h - inter;
	return uni > 0 ? inter / uni : 0;
}

// Greedy NMS over (already score-filtered) candidates.
std::vector<BoxScore> NMS(std::vector<BoxScore> cands, float iou_thresh) {
	std::sort(cands.begin(), cands.end(),
			  [](const BoxScore& a, const BoxScore& b) { return a.score > b.score; });
	std::vector<BoxScore> keep;
	for (const auto& c : cands) {
		bool suppressed = false;
		for (const auto& k : keep) {
			if (IoU(c, k) > iou_thresh) {
				suppressed = true;
				break;
			}
		}
		if (!suppressed) keep.push_back(c);
	}
	return keep;
}

} // namespace

struct NcnnBackend::Impl {
	int input_size = 640;
	float score_thresh = 0.5f;
	float iou_thresh = 0.45f;
	int class_index = 0; // COCO person
	ncnn::Net net;
	std::mutex net_mutex;             // one shared net = one GPU worker (AD-3)
	std::chrono::steady_clock::time_point opened;
	int64_t total_infer_us = 0;
	int64_t infer_calls = 0;
	int64_t last_infer_us = 0;
};

NcnnBackend::~NcnnBackend() = default;

std::unique_ptr<NcnnBackend> NcnnBackend::Open(
	const std::string& model_dir, const std::string& model_name,
	int input_size, float score_thresh, float iou_thresh, int class_index,
	bool prefer_vulkan) {
	auto be = std::unique_ptr<NcnnBackend>(new NcnnBackend());
	be->impl_ = std::make_unique<Impl>();
	Impl& im = *be->impl_;
	im.input_size = input_size;
	im.score_thresh = score_thresh;
	im.iou_thresh = iou_thresh;
	im.class_index = class_index;

	bool use_vulkan = false;
#ifdef NCNN_VULKAN
	use_vulkan = prefer_vulkan && ncnn::get_gpu_count() > 0;
#endif
	im.net.opt.use_vulkan_compute = use_vulkan;
	im.net.opt.num_threads = use_vulkan ? 1 : 4; // GPU: feeding thread only
	be->backend_name_ = use_vulkan ? "vulkan" : "cpu";

	const std::string param = model_dir + "/" + model_name + ".param";
	const std::string bin = model_dir + "/" + model_name + ".bin";
	if (im.net.load_param(param.c_str()) != 0) return nullptr;
	if (im.net.load_model(bin.c_str()) != 0) return nullptr;
	im.opened = std::chrono::steady_clock::now();
	return be;
}

std::vector<Detection> NcnnBackend::Infer(const Frame& f) {
	Impl& im = *impl_;
	const int target = im.input_size;

	// Plain resize to square (matches ultralytics ncnn-export examples);
	// letterbox refinement is a tuning step, not a blocker.
	std::vector<uint8_t> bgr3;
	ncnn::Mat in;
	if (f.is_gray) {
		// YOLO expects 3 channels; expand gray → BGR(3x same).
		bgr3.resize(size_t(f.width) * f.height * 3);
		for (size_t i = 0; i < size_t(f.width) * f.height; ++i) {
			bgr3[i * 3 + 0] = bgr3[i * 3 + 1] = bgr3[i * 3 + 2] = f.data[i];
		}
		in = ncnn::Mat::from_pixels_resize(bgr3.data(), ncnn::Mat::PIXEL_BGR2RGB,
										   f.width, f.height, target, target);
	} else {
		in = ncnn::Mat::from_pixels_resize(f.data.data(), ncnn::Mat::PIXEL_BGR2RGB,
										   f.width, f.height, target, target);
	}
	const float norm[3] = {1 / 255.f, 1 / 255.f, 1 / 255.f};
	in.substract_mean_normalize(nullptr, norm);

	std::lock_guard<std::mutex> lk(im.net_mutex); // serialize GPU access (AD-3)
	auto t0 = std::chrono::steady_clock::now();
	ncnn::Extractor ex = im.net.create_extractor();
	ex.input("in0", in);
	ncnn::Mat out;
	if (ex.extract("out0", out) != 0) return {};
	auto t1 = std::chrono::steady_clock::now();
	im.last_infer_us =
		std::chrono::duration_cast<std::chrono::microseconds>(t1 - t0).count();
	im.total_infer_us += im.last_infer_us;
	im.infer_calls++;

	// YOLO11 output: (4 + num_classes) x N — either orientation appears
	// depending on export; detect by matching class row/col presence.
	const int channels = out.c;
	const int rows_h = out.h, cols_w = out.w;
	std::vector<BoxScore> cands;
	cands.reserve(256);

	// Multi-class decode (B2): collect every (class, box) pair above the
	// score threshold, then NMS per class. Orientation A: rows 0..3 are box
	// params, rows 4.. are class scores (COCO order).
	if (rows_h >= 5 && cols_w >= 5 && channels == 1) {
		const int nc = rows_h - 4;
		std::vector<BoxScore> raw;
		raw.reserve(512);
		for (int ci = 0; ci < nc; ++ci) {
			const float* scores = out.row(4 + ci);
			const float* cx = out.row(0);
			const float* cy = out.row(1);
			const float* bw = out.row(2);
			const float* bh = out.row(3);
			for (int i = 0; i < cols_w; ++i) {
				float score = scores[i];
				if (score < im.score_thresh) continue;
				BoxScore b;
				b.x = (cx[i] - bw[i] / 2.f) * float(f.width) / target;
				b.y = (cy[i] - bh[i] / 2.f) * float(f.height) / target;
				b.w = bw[i] * float(f.width) / target;
				b.h = bh[i] * float(f.height) / target;
				b.score = score;
				b.cls = ci;
				raw.push_back(b);
			}
		}
		for (int ci = 0; ci < nc; ++ci) {
			std::vector<BoxScore> per;
			for (const auto& b : raw)
				if (b.cls == ci) per.push_back(b);
			for (const auto& k : NMS(std::move(per), im.iou_thresh)) cands.push_back(k);
		}
	}
	// Orientation B: cols are params (4+nc), rows are candidates.
	else if (cols_w >= 5 && rows_h >= 5) {
		const int nc = cols_w - 4;
		std::vector<BoxScore> raw;
		raw.reserve(512);
		for (int i = 0; i < rows_h; ++i) {
			const float* row = out.row(i);
			for (int ci = 0; ci < nc; ++ci) {
				float score = row[ci + 4];
				if (score < im.score_thresh) continue;
				BoxScore b;
				b.x = (row[0] - row[2] / 2.f) * float(f.width) / target;
				b.y = (row[1] - row[3] / 2.f) * float(f.height) / target;
				b.w = row[2] * float(f.width) / target;
				b.h = row[3] * float(f.height) / target;
				b.score = score;
				b.cls = ci;
				raw.push_back(b);
			}
		}
		for (int ci = 0; ci < nc; ++ci) {
			std::vector<BoxScore> per;
			for (const auto& b : raw)
				if (b.cls == ci) per.push_back(b);
			for (const auto& k : NMS(std::move(per), im.iou_thresh)) cands.push_back(k);
		}
	}

	std::vector<Detection> dets;
	for (const auto& b : NMS(std::move(cands), im.iou_thresh)) {
		Detection d;
		d.x = std::max(0.f, b.x);
		d.y = std::max(0.f, b.y);
		d.w = b.w;
		d.h = b.h;
		d.score = b.score;
		const auto& names = cocoNames();
		d.label = (b.cls >= 0 && b.cls < (int)names.size()) ? names[b.cls] : "object";
		dets.push_back(d);
	}
	return dets;
}

double NcnnBackend::lastInferMs() const {
	const Impl& im = *impl_;
	return double(im.last_infer_us) / 1000.0;
}

double NcnnBackend::avgInferMs() const {
	const Impl& im = *impl_;
	return im.infer_calls == 0
			   ? 0
			   : double(im.total_infer_us) / 1000.0 / double(im.infer_calls);
}

} // namespace nvr
