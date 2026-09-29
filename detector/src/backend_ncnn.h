// NCNN inference backend (FR-DET-3/4): YOLO11n via NCNN, Vulkan (Adreno 640)
// when a GPU device is available, CPU/XNNPACK otherwise (FLAG-12 fallback).
// Person-class filter + score threshold + NMS. Requires NVR_WITH_NCNN.
#pragma once

#include "backend.h"

#include <memory>
#include <string>

namespace nvr {

class NcnnBackend : public InferenceBackend {
public:
	~NcnnBackend() override; // out-of-line: Impl is incomplete here (pimpl)
	// model_dir holds <model>.param / <model>.bin (FRS §6.3: detection.model
	// resolved under base_path/models). input_size is square (640 default).
	// class_index selects the COCO class (0 = person).
	static std::unique_ptr<NcnnBackend> Open(const std::string& model_dir,
											 const std::string& model_name,
											 int input_size, float score_thresh,
											 float iou_thresh, int class_index,
											 bool prefer_vulkan);

	std::vector<Detection> Infer(const Frame& f) override;
	std::string Name() const override { return backend_name_; }

	// For tests/benchmarks: average inference ms since open (FR-DET-12).
	double avgInferMs() const;
	// Per-call inference ms (steady-state timing).
	double lastInferMs() const;

private:
	struct Impl;
	std::unique_ptr<Impl> impl_;
	std::string backend_name_;
};

} // namespace nvr
