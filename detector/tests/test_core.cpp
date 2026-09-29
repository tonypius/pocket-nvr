// Dependency-free unit tests for the detector core (FR-DET-2/5/6/7).
#include "backend.h"
#include "event_state.h"
#include "frame_queue.h"
#include "motion_gate.h"
#include "zones.h"

#include <cassert>
#include <cstdio>
#include <thread>
#include <vector>

using namespace nvr;

static int failures = 0;
#define CHECK(cond)                                                    \
	do {                                                               \
		if (!(cond)) {                                                 \
			++failures;                                                \
			std::printf("FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond); \
		}                                                              \
	} while (0)

static Frame grayFrame(int w, int h, uint8_t fill, int64_t ts) {
	Frame f;
	f.width = w;
	f.height = h;
	f.is_gray = true;
	f.ts_ms = ts;
	f.data.assign(size_t(w) * h, fill);
	return f;
}

static void testMotionGate() {
	// Static scene: identical frames never trigger (FR-DET-2).
	MotionGate gate(0.02f);
	Frame a = grayFrame(640, 360, 100, 1);
	CHECK(gate.Process(a.data.data(), a.width, a.height) == false); // seed
	CHECK(gate.Process(a.data.data(), a.width, a.height) == false);
	CHECK(gate.Process(a.data.data(), a.width, a.height) == false);

	// Small change below threshold area → no trigger.
	Frame b = a;
	b.ts_ms = 2;
	for (int y = 0; y < 5; ++y)
		for (int x = 0; x < 640; ++x) b.data[size_t(y) * 640 + x] = 250;
	CHECK(gate.Process(b.data.data(), b.width, b.height) == false);

	// Big change (>2% of frame) → trigger.
	Frame c = a;
	c.ts_ms = 3;
	for (int y = 0; y < 100; ++y)
		for (int x = 0; x < 200; ++x) c.data[size_t(y) * 640 + x] = 250;
	bool hit = gate.Process(c.data.data(), c.width, c.height);
	CHECK(hit);
	CHECK(gate.lastChangedArea() >= 0.02f);
}

static void testZones() {
	// FRS §6.3 driveway zone (pixel coords, 640x360 frame).
	Zone z{"driveway", {{0, 180}, {640, 180}, {640, 360}, {0, 360}}};
	std::vector<Zone> zones{z};

	// bbox fully inside zone, bottom-center anchor inside.
	float inside[4] = {100, 200, 80, 150}; // bottom y=350 < 360
	CHECK(DetectionInZones(inside, Anchor::BottomCenter, zones));

	// bbox above the zone line: feet at y=170 → outside.
	float above[4] = {100, 20, 80, 150};
	CHECK(!DetectionInZones(above, Anchor::BottomCenter, zones));

	// centroid of that box (y=95) is also above the 180-line → outside.
	CHECK(!DetectionInZones(above, Anchor::Centroid, zones));

	// Empty zones = whole frame.
	CHECK(DetectionInZones(above, Anchor::BottomCenter, {}));

	// PointInPolygon sanity: concave quad (square with a V-notch at the
	// bottom edge, notch tip at (50,50)).
	std::vector<std::pair<float, float>> concave = {
		{0, 0}, {100, 0}, {100, 100}, {50, 50}, {0, 100}};
	CHECK(PointInPolygon(25, 25, concave));
	CHECK(PointInPolygon(75, 60, concave)); // above the V edge (y<x) → inside
	CHECK(!PointInPolygon(75, 80, concave)); // in the notch (y>x) → outside
	CHECK(!PointInPolygon(150, 50, concave));
}

static void testEventStateMachine() {
	// enter after 3 positives, exit after 8 negatives (defaults).
	EventStateMachine sm(3, 8);
	float bbox[4] = {10, 20, 30, 40};
	int64_t t = 1000;

	// Flicker: 2 positives then negative → nothing (FR-DET-7).
	CHECK(sm.OnFrame(true, 0.5f, bbox, t) == EventSignal::None);
	CHECK(sm.OnFrame(true, 0.6f, bbox, t + 200) == EventSignal::None);
	CHECK(sm.OnFrame(false, 0, bbox, t + 400) == EventSignal::None);
	CHECK(!sm.active());

	// Real entry: 3 consecutive positives; start ts = first positive.
	CHECK(sm.OnFrame(true, 0.5f, bbox, t + 600) == EventSignal::None);
	CHECK(sm.OnFrame(true, 0.6f, bbox, t + 800) == EventSignal::None);
	CHECK(sm.OnFrame(true, 0.7f, bbox, t + 1000) == EventSignal::Entered);
	CHECK(sm.startTs() == t + 600);
	CHECK(sm.active());

	// Ongoing presence: one event, peak tracked (FR-DET-7).
	CHECK(sm.OnFrame(true, 0.9f, bbox, t + 1200) == EventSignal::Active);
	CHECK(sm.OnFrame(true, 0.4f, bbox, t + 1400) == EventSignal::Active);
	CHECK(sm.peakScore() == 0.9f);

	// Brief gap (< exit_frames) does NOT close the event.
	for (int i = 1; i <= 7; ++i)
		CHECK(sm.OnFrame(false, 0, bbox, t + 1600 + i * 200) == EventSignal::Active);
	CHECK(sm.OnFrame(true, 0.5f, bbox, t + 3200) == EventSignal::Active);

	// 8 consecutive negatives → Exited with peak info.
	for (int i = 1; i <= 8; ++i) {
		EventSignal s = sm.OnFrame(false, 0, bbox, t + 3400 + i * 200);
		CHECK(i < 8 ? s == EventSignal::Active : s == EventSignal::Exited);
	}
	const EventRecord& e = sm.completed();
	CHECK(e.start_ts == t + 600);
	CHECK(e.end_ts == t + 3400 + 8 * 200);
	CHECK(e.peak_score == 0.9f);
	CHECK(!sm.active());
}

static void testFrameQueue() {
	// Drop-oldest under overflow (FR-DET-5): newest frames win, count bounded.
	FrameQueue q(4);
	for (int i = 0; i < 10; ++i) {
		Frame f = grayFrame(4, 4, uint8_t(i), i);
		q.Push(std::move(f));
	}
	CHECK(q.size() == 4);
	CHECK(q.dropped() == 6);
	Frame out;
	// oldest surviving frame is #6
	CHECK(q.Pop(out, 0));
	CHECK(out.ts_ms == 6);

	// Consumer/producer across threads, then Stop wakes the consumer.
	FrameQueue q2(8);
	std::thread producer([&] {
		for (int i = 0; i < 20; ++i) {
			Frame f = grayFrame(4, 4, 1, i);
			q2.Push(std::move(f));
		}
		q2.Stop();
	});
	int consumed = 0;
	Frame f;
	while (true) {
		if (q2.Pop(f, 50)) {
			++consumed;
			continue;
		}
		if (q2.isStopped()) break; // timeout before Stop is possible → retry
	}
	producer.join();
	// Invariant: nothing lost — consumed + dropped-oldest == pushed. With a
	// cap of 8 and 20 pushes, most frames are intentionally dropped.
	CHECK(consumed + (int)q2.dropped() == 20);
	CHECK(q2.dropped() > 0);
	CHECK(q2.Push(grayFrame(2, 2, 1, 1)) == false); // stopped queue rejects
}

static void testMockBackend() {
	float box[4] = {1, 1, 2, 2};
	(void)box;
	Detection d1;
	d1.score = 0.5f;
	d1.label = "person";
	Detection d2;
	d2.score = 0.9f;
	d2.label = "person";
	MockBackend::Response resp1{{d1}}, resp2{{d2}};
	MockBackend be({resp1, resp2});
	Frame f;
	auto r0 = be.Infer(f);
	CHECK(r0.size() == 1 && r0[0].score == 0.5f);
	auto r1 = be.Infer(f);
	CHECK(r1.size() == 1 && r1[0].score == 0.9f);
	auto r2 = be.Infer(f); // repeats last
	CHECK(r2.size() == 1 && r2[0].score == 0.9f);
	CHECK(be.calls() == 2);
}

int main() {
	testMotionGate();
	testZones();
	testEventStateMachine();
	testFrameQueue();
	testMockBackend();
	if (failures == 0) {
		std::printf("ALL DETECTOR CORE TESTS PASSED\n");
		return 0;
	}
	std::printf("%d FAILURE(S)\n", failures);
	return 1;
}
