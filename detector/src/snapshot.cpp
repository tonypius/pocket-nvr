#include "snapshot.h"

#include <algorithm>
#include <cstring>

#define STB_IMAGE_WRITE_IMPLEMENTATION
#define STBI_WRITE_NO_STDIO
#include "stb_image_write.h"

namespace nvr {

namespace {

void drawRect(Frame& f, float x, float y, float w, float h) {
	const int t = 2;
	auto px = [&](int px_x, int px_y) {
		if (px_x < 0 || px_y < 0 || px_x >= f.width || px_y >= f.height) return;
		size_t i = (size_t(px_y) * f.width + px_x) * 3;
		f.data[i] = 0;     // B
		f.data[i + 1] = 255; // G
		f.data[i + 2] = 0;  // R
	};
	int x0 = int(x), y0 = int(y), x1 = int(x + w), y1 = int(y + h);
	for (int yy = y0; yy <= y1; ++yy)
		for (int k = 0; k < t; ++k) {
			px(x0 + k, yy);
			px(x1 - k, yy);
		}
	for (int xx = x0; xx <= x1; ++xx)
		for (int k = 0; k < t; ++k) {
			px(xx, y0 + k);
			px(xx, y1 - k);
		}
}

void stbCallback(void* ctx, void* data, int size) {
	auto* out = static_cast<std::vector<uint8_t>*>(ctx);
	auto* bytes = static_cast<uint8_t*>(data);
	out->insert(out->end(), bytes, bytes + size);
}

} // namespace

std::vector<uint8_t> encodeCropJpeg(const Frame& f, const float bbox[4]) {
	float x = bbox[0], y = bbox[1], w = bbox[2], h = bbox[3];
	// 12% padding around the box
	float pw = w * 0.12f, ph = h * 0.12f;
	x = std::max(0.f, x - pw);
	y = std::max(0.f, y - ph);
	w = std::min(f.width - x, w + pw * 2);
	h = std::min(f.height - y, h + ph * 2);

	int ix = int(x), iy = int(y);
	int iw = int(w), ih = int(h);
	if (iw < 8 || ih < 8) return {};

	std::vector<uint8_t> crop(size_t(iw) * ih * 3);
	for (int r = 0; r < ih; ++r) {
		std::memcpy(&crop[size_t(r) * iw * 3],
					&f.data[(size_t(iy + r) * f.width + ix) * 3], size_t(iw) * 3);
	}
	// scale so the long edge is 320px
	float scale = 320.f / float(iw > ih ? iw : ih);
	int tw = int(iw * scale), th = int(ih * scale);
	if (tw < 1) tw = 1;
	if (th < 1) th = 1;

	std::vector<uint8_t> scaled(size_t(tw) * th * 3);
	for (int r = 0; r < th; ++r) {
		int sy = r * ih / th;
		for (int c = 0; c < tw; ++c) {
			int sx = c * iw / tw;
			std::memcpy(&scaled[(size_t(r) * tw + c) * 3],
						&crop[(size_t(sy) * iw + sx) * 3], 3);
		}
	}

	std::vector<uint8_t> jpeg;
	stbi_write_jpg_to_func(stbCallback, &jpeg, tw, th, 3, scaled.data(), 85);
	return jpeg;
}

std::vector<uint8_t> encodeSnapshotJpeg(const Frame& f, const float bbox[4]) {
	Frame copy = f; // one copy per event peak — cheap (sub-stream frame)
	drawRect(copy, bbox[0], bbox[1], bbox[2], bbox[3]);
	std::vector<uint8_t> jpeg;
	stbi_write_jpg_to_func(stbCallback, &jpeg, copy.width, copy.height, 3,
						   copy.data.data(), 85);
	return jpeg;
}

} // namespace nvr
