#include "zones.h"

namespace nvr {

bool PointInPolygon(float px, float py,
					const std::vector<std::pair<float, float>>& poly) {
	if (poly.size() < 3) return false;
	bool inside = false;
	const size_t n = poly.size();
	for (size_t i = 0, j = n - 1; i < n; j = i++) {
		float xi = poly[i].first, yi = poly[i].second;
		float xj = poly[j].first, yj = poly[j].second;
		if (((yi > py) != (yj > py)) &&
			(px < (xj - xi) * (py - yi) / (yj - yi) + xi)) {
			inside = !inside;
		}
	}
	return inside;
}

void AnchorPoint(const float bbox[4], Anchor mode, float* ax, float* ay) {
	if (mode == Anchor::Centroid) {
		*ax = bbox[0] + bbox[2] / 2.0f;
		*ay = bbox[1] + bbox[3] / 2.0f;
	} else { // BottomCenter: feet position — standard for ground zones
		*ax = bbox[0] + bbox[2] / 2.0f;
		*ay = bbox[1] + bbox[3];
	}
}

bool DetectionInZones(const float bbox[4], Anchor mode,
					  const std::vector<Zone>& zones) {
	if (zones.empty()) return true; // FR-DET-6: empty = whole frame
	float ax = 0, ay = 0;
	AnchorPoint(bbox, mode, &ax, &ay);
	for (const auto& z : zones) {
		if (PointInPolygon(ax, ay, z.polygon)) return true;
	}
	return false;
}

} // namespace nvr
