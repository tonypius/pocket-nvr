// Runtime config for the detector daemon (nvrdet), rendered by nvrd from
// config.yaml (see internal/detectorcfg). Parsed with vendored nlohmann.
#pragma once

#include "types.h"

#include <stdexcept>
#include <string>
#include <vector>

namespace nvr {

struct CamCfg {
	std::string id, name, sub_url;
	std::vector<std::string> classes;
	int fps = 5;
	float threshold = 0.5f;
	int enter = 3, exit_frames = 8;
	float motion_area = 0.02f;
	bool bottom_center = true; // anchor
	std::vector<Zone> zones;
};

struct DetConfig {
	std::string api_url, api_token;
	std::string model_dir, model = "yolo11n", backend = "vulkan";
	int input_size = 640, queue_max = 32;
	int temp_high = 75, temp_low = 65;
	int frame_w = 640, frame_h = 360;
	std::vector<CamCfg> cameras;
};

// Parses detector.json; throws std::runtime_error with a clear message on
// malformed input (FR-CFG-3 spirit: refuse to start on bad config).
DetConfig loadConfig(const std::string& path);

} // namespace nvr
