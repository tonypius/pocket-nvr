// Inference backend interface (FR-DET-3/4). Implementations: NcnnVulkan /
// NcnnCpu (phone, Phase 3 wiring) and MockBackend (unit tests).
#pragma once

#include "types.h"

#include <string>
#include <utility>
#include <vector>

namespace nvr {

class InferenceBackend {
public:
	virtual ~InferenceBackend() = default;
	// Run the model on one frame; returns person detections in frame coords.
	virtual std::vector<Detection> Infer(const Frame& f) = 0;
	// "vulkan" | "cpu" | "mock" — surfaced in metrics/logs (FR-DET-3).
	virtual std::string Name() const = 0;
};

// Scripted detections for tests: for each Infer call returns the next
// entry (repeating the last one when exhausted).
class MockBackend : public InferenceBackend {
public:
	struct Response {
		std::vector<Detection> dets;
	};
	explicit MockBackend(std::vector<Response> script) : script_(std::move(script)) {}

	std::vector<Detection> Infer(const Frame&) override {
		size_t idx = calls_;
		if (calls_ < script_.size()) ++calls_;
		if (script_.empty()) return {};
		return script_[idx < script_.size() ? idx : script_.size() - 1].dets;
	}
	std::string Name() const override { return "mock"; }
	size_t calls() const { return calls_; }

private:
	std::vector<Response> script_;
	size_t calls_ = 0;
};

} // namespace nvr
