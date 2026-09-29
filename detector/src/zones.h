// Zones (FR-DET-6): a detection counts only if its anchor point
// (bottom-center or centroid of the bbox) falls inside an active zone
// polygon. Empty zone list = whole frame active.
#pragma once

#include "types.h"

#include <string>
#include <vector>

namespace nvr {

enum class Anchor {
	BottomCenter,
	Centroid,
};

// Ray-cast point-in-polygon.
bool PointInPolygon(float px, float py, const std::vector<std::pair<float, float>>& poly);

// Anchor point of a bbox in frame pixel space.
void AnchorPoint(const float bbox[4], Anchor mode, float* ax, float* ay);

// True when the bbox anchor is inside at least one zone (or zones empty).
bool DetectionInZones(const float bbox[4], Anchor mode,
					  const std::vector<Zone>& zones);

} // namespace nvr
