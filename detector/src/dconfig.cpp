#include "dconfig.h"

#include <nlohmann/json.hpp>

namespace nvr {

DetConfig loadConfig(const std::string& path) {
	std::FILE* f = std::fopen(path.c_str(), "rb");
	if (!f) throw std::runtime_error("cannot open " + path);
	std::string buf;
	char tmp[65536];
	size_t n;
	while ((n = std::fread(tmp, 1, sizeof tmp, f)) > 0) buf.append(tmp, n);
	std::fclose(f);

	nlohmann::json j = nlohmann::json::parse(buf); // throws with position info
	DetConfig c;
	c.api_url = j.at("api").at("url").get<std::string>();
	c.api_token = j.at("api").at("token").get<std::string>();

	const auto& d = j.at("detection");
	c.model_dir = d.at("model_dir").get<std::string>();
	c.model = d.value("model", c.model);
	c.backend = d.value("backend", c.backend);
	c.input_size = d.value("input_size", c.input_size);
	c.queue_max = d.value("queue_max", c.queue_max);
	c.temp_high = d.value("temp_high_c", c.temp_high);
	c.temp_low = d.value("temp_low_c", c.temp_low);

	c.frame_w = j.at("frame").value("width", c.frame_w);
	c.frame_h = j.at("frame").value("height", c.frame_h);

	for (const auto& jc : j.at("cameras")) {
		CamCfg cam;
		cam.id = jc.at("id").get<std::string>();
		cam.name = jc.value("name", cam.id);
		cam.sub_url = jc.at("sub_url").get<std::string>();
		cam.fps = jc.value("fps", 5);
		cam.threshold = jc.value("threshold", 0.5f);
		cam.enter = jc.value("enter_frames", 3);
		cam.exit_frames = jc.value("exit_frames", 8);
		cam.motion_area = jc.value("motion_min_area", 0.02f);
		cam.bottom_center = jc.value("anchor", std::string("bottom_center")) == "bottom_center";
		if (jc.contains("classes")) {
			for (const auto& c : jc.at("classes")) cam.classes.push_back(c.get<std::string>());
		}
		if (cam.classes.empty()) cam.classes.push_back("person");
		if (jc.contains("zones")) {
			for (const auto& jz : jc.at("zones")) {
				Zone z;
				z.name = jz.at("name").get<std::string>();
				for (const auto& pt : jz.at("polygon")) {
					z.polygon.push_back({pt[0].get<float>(), pt[1].get<float>()});
				}
				cam.zones.push_back(std::move(z));
			}
		}
		c.cameras.push_back(std::move(cam));
	}
	if (c.api_token.empty()) throw std::runtime_error("api.token empty");
	if (c.cameras.empty()) throw std::runtime_error("no cameras in config");
	return c;
}

} // namespace nvr
