package ptz

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pocketnvr/internal/config"
	"pocketnvr/internal/ptz/ptztest"
)

// newManager returns a Manager with shrunk timeouts. Its cleanup resets it and
// fails the test if any timer or background goroutine outlives Reset.
func newManager(t *testing.T, maxMove time.Duration) *Manager {
	t.Helper()
	m := NewManager(nil)
	m.maxMove = maxMove
	m.stopRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		m.Reset()
		done := make(chan struct{})
		go func() { m.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("timers or goroutines still running after Reset")
		}
	})
	return m
}

func camFor(c *ptztest.Camera) config.Camera {
	return config.Camera{ID: "c1", SourceMain: "rtsp://u:p@127.0.0.1/x", OnvifURL: c.URL}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ptzFake(t *testing.T) *ptztest.Camera {
	return newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
}

func TestSafetyStopAfterMove(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, 150*time.Millisecond)
	// The request's context is long gone by the time the timer fires; the
	// stop must not depend on it.
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Move(ctx, camFor(fake), Left); err != nil {
		t.Fatalf("Move: %v", err)
	}
	cancel()
	eventually(t, "safety stop", func() bool { return fake.Stops() >= 1 })
	time.Sleep(400 * time.Millisecond)
	if got := fake.Stops(); got != 1 {
		t.Errorf("Stops = %d, want exactly 1", got)
	}
	mv := fake.Moves()
	if len(mv) != 1 || mv[0].Pan >= 0 || mv[0].Timeout != "PT0.15S" {
		t.Errorf("Moves = %+v, want one leftward move with the safety timeout", mv)
	}
}

func TestRepeatedMoveKeepsMovingThenStops(t *testing.T) {
	fake := ptzFake(t)
	const maxMove = 300 * time.Millisecond
	m := newManager(t, maxMove)
	cam := camFor(fake)
	// Three Moves 200 ms apart: the whole hold (400 ms) outlasts maxMove, so
	// only a re-arm on every Move keeps the camera from being stopped midway.
	var first time.Time
	for i := 0; i < 3; i++ {
		if err := m.Move(context.Background(), cam, Right); err != nil {
			t.Fatalf("Move %d: %v", i, err)
		}
		if i == 0 {
			first = time.Now()
		}
		if i < 2 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	last := time.Now()
	// Now at ~first+400ms: after first+maxMove, before last+maxMove (~100 ms
	// from the first deadline having passed, ~300 ms before the last one).
	if got := fake.Stops(); got != 0 {
		t.Fatalf("Stops = %d at %v after the first Move (maxMove %v) while still held, want 0: a Move did not re-arm the timer",
			got, last.Sub(first).Round(time.Millisecond), maxMove)
	}
	eventually(t, "safety stop after the last Move", func() bool { return fake.Stops() >= 1 })
	if d := time.Since(last); d < maxMove-50*time.Millisecond {
		t.Errorf("stop arrived %v after the last Move, want about maxMove (%v)", d, maxMove)
	}
	time.Sleep(900 * time.Millisecond)
	if got := fake.Stops(); got != 1 {
		t.Errorf("Stops = %d, want exactly 1", got)
	}
	if got := len(fake.Moves()); got != 3 {
		t.Errorf("Moves = %d, want 3", got)
	}
}

func TestStopCancelsSafetyTimer(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, 600*time.Millisecond)
	cam := camFor(fake)
	if err := m.Move(context.Background(), cam, Up); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if err := m.Stop(context.Background(), cam); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := fake.Stops(); got != 1 {
		t.Errorf("Stops = %d, want 1 (timer must be cancelled)", got)
	}
}

func TestCommandsAreSerialised(t *testing.T) {
	fake := ptzFake(t)
	// A tiny maxMove makes safety timers fire in the middle of the storm too.
	m := newManager(t, 5*time.Millisecond)
	cam := camFor(fake)
	if _, err := m.Caps(context.Background(), cam); err != nil {
		t.Fatalf("Caps: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_ = m.Move(context.Background(), cam, Left)
			} else {
				_ = m.Stop(context.Background(), cam)
			}
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond) // let late safety stops land
	if got := fake.MaxConcurrent(); got != 1 {
		t.Errorf("MaxConcurrent = %d, want 1", got)
	}
	if fake.Stops() == 0 || len(fake.Moves()) == 0 {
		t.Errorf("expected both moves and stops, got %d moves %d stops", len(fake.Moves()), fake.Stops())
	}
}

func TestMoveOnNonPTZ(t *testing.T) {
	fake := newCam(t, ptztest.Options{NoPTZService: true})
	m := newManager(t, time.Second)
	cam := camFor(fake)
	if err := m.Move(context.Background(), cam, Up); !errors.Is(err, ErrNotPTZ) {
		t.Errorf("Move err = %v, want ErrNotPTZ", err)
	}
	if err := m.Stop(context.Background(), cam); !errors.Is(err, ErrNotPTZ) {
		t.Errorf("Stop err = %v, want ErrNotPTZ", err)
	}
	if got := fake.Moves(); len(got) != 0 {
		t.Errorf("Moves = %+v, want none", got)
	}
	if got := fake.Stops(); got != 0 {
		t.Errorf("Stops = %d, want 0", got)
	}
}

func TestMoveUnsupportedAxis(t *testing.T) {
	cases := []struct {
		name string
		opts ptztest.Options
		dir  Direction
	}{
		{"zoom on pan/tilt-only", ptztest.Options{PanTilt: true}, ZoomIn},
		{"zoom out on pan/tilt-only", ptztest.Options{PanTilt: true}, ZoomOut},
		{"pan on zoom-only", ptztest.Options{Zoom: true}, Left},
		{"tilt on zoom-only", ptztest.Options{Zoom: true}, Up},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newCam(t, tc.opts)
			m := newManager(t, time.Second)
			if err := m.Move(context.Background(), camFor(fake), tc.dir); !errors.Is(err, ErrUnsupported) {
				t.Errorf("Move err = %v, want ErrUnsupported", err)
			}
			if got := fake.Moves(); len(got) != 0 {
				t.Errorf("Moves = %+v, want none", got)
			}
		})
	}
	// The supported axes do work.
	fake := newCam(t, ptztest.Options{PanTilt: true})
	m := newManager(t, time.Second)
	if err := m.Move(context.Background(), camFor(fake), Down); err != nil {
		t.Errorf("Move Down on pan/tilt-only: %v", err)
	}
}

// blackhole listens but never answers, like a camera that dropped off the
// network after accepting connections.
func blackhole(t *testing.T) config.Camera {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return config.Camera{ID: "dead", SourceMain: "rtsp://u:p@127.0.0.1/x", OnvifURL: "http://" + l.Addr().String() + "/onvif/device_service"}
}

func TestUnreachableCameraProbeIsBounded(t *testing.T) {
	cam := blackhole(t)
	m := newManager(t, time.Second)
	m.probeTimeout = 300 * time.Millisecond

	start := time.Now()
	if caps, ok := m.CapsCached(cam); ok || caps != (Caps{}) {
		t.Errorf("CapsCached = %+v, %v; want empty, false", caps, ok)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("CapsCached took %v, want < 50ms", d)
	}

	start = time.Now()
	caps, err := m.Caps(context.Background(), cam)
	if err == nil {
		t.Error("Caps err = nil, want an error")
	}
	if caps != (Caps{}) {
		t.Errorf("Caps = %+v, want empty", caps)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("Caps took %v, want about the 300ms probe timeout", d)
	}

	// Move on the unreachable camera fails fast from the cached failure.
	start = time.Now()
	if err := m.Move(context.Background(), cam, Up); err == nil {
		t.Error("Move err = nil, want the cached failure")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Move on cached failure took %v", d)
	}
}

// countingServer answers every request with an HTTP 500 and counts them.
func countingServer(t *testing.T) (config.Camera, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return config.Camera{ID: "bad", SourceMain: "rtsp://u:p@127.0.0.1/x", OnvifURL: srv.URL + "/onvif/device_service"}, &n
}

func TestFailureIsCachedBriefly(t *testing.T) {
	cam, hits := countingServer(t)
	m := newManager(t, time.Second)
	m.failureTTL = 400 * time.Millisecond

	if _, err := m.Caps(context.Background(), cam); err == nil {
		t.Fatal("Caps err = nil, want failure")
	}
	first := hits.Load()
	if first == 0 {
		t.Fatal("expected the probe to reach the server")
	}
	if _, err := m.Caps(context.Background(), cam); err == nil {
		t.Error("second Caps err = nil, want the cached failure")
	}
	if err := m.Move(context.Background(), cam, Up); err == nil {
		t.Error("Move err = nil, want the cached failure")
	}
	if got := hits.Load(); got != first {
		t.Errorf("requests went from %d to %d: failure not cached", first, got)
	}
	// ...but only briefly.
	eventually(t, "failure TTL to expire and the camera to be probed again", func() bool {
		_, _ = m.Caps(context.Background(), cam)
		return hits.Load() > first
	})
}

func TestWrongPasswordIsCachedFailure(t *testing.T) {
	fake := ptzFake(t)
	cam := camFor(fake)
	cam.SourceMain = "rtsp://u:wrong@127.0.0.1/x"
	m := newManager(t, time.Second)
	_, err := m.Caps(context.Background(), cam)
	if err == nil {
		t.Fatal("Caps err = nil, want auth failure")
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("error leaks the password: %v", err)
	}
	n := fake.Requests()
	if _, err := m.Caps(context.Background(), cam); err == nil {
		t.Error("second Caps err = nil")
	}
	if got := fake.Requests(); got != n {
		t.Errorf("requests %d -> %d: failure not cached", n, got)
	}
}

func TestNoPTZCameraIsCachedAsSuccess(t *testing.T) {
	fake := newCam(t, ptztest.Options{NoPTZService: true})
	m := newManager(t, time.Second)
	cam := camFor(fake)
	caps, err := m.Caps(context.Background(), cam)
	if err != nil || caps != (Caps{}) {
		t.Fatalf("Caps = %+v, %v; want empty, nil", caps, err)
	}
	n := fake.Requests()
	if caps, ok := m.CapsCached(cam); !ok || caps != (Caps{}) {
		t.Errorf("CapsCached = %+v, %v; want empty, true", caps, ok)
	}
	if _, err := m.Caps(context.Background(), cam); err != nil {
		t.Errorf("second Caps: %v", err)
	}
	if got := fake.Requests(); got != n {
		t.Errorf("requests %d -> %d: result not cached", n, got)
	}
}

// probeRequests is how many SOAP requests one full probe of a PTZ fake makes.
func probeRequests(t *testing.T) int {
	t.Helper()
	fake := ptzFake(t)
	m := newManager(t, time.Second)
	if _, err := m.Caps(context.Background(), camFor(fake)); err != nil {
		t.Fatal(err)
	}
	return fake.Requests()
}

func TestCapsSuccessCachedThenExpires(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, time.Second)
	m.capsTTL = 300 * time.Millisecond
	cam := camFor(fake)
	caps, err := m.Caps(context.Background(), cam)
	if err != nil || caps != (Caps{PanTilt: true, Zoom: true}) {
		t.Fatalf("Caps = %+v, %v", caps, err)
	}
	n := fake.Requests()
	if got, ok := m.CapsCached(cam); !ok || got != caps {
		t.Errorf("CapsCached = %+v, %v", got, ok)
	}
	if got := fake.Requests(); got != n {
		t.Errorf("cache hit made requests: %d -> %d", n, got)
	}
	eventually(t, "caps TTL to expire", func() bool {
		_, ok := m.CapsCached(cam)
		return !ok
	})
	// CapsCached started a background re-probe, which refreshes the cache.
	eventually(t, "background re-probe", func() bool {
		_, ok := m.CapsCached(cam)
		return ok
	})
}

func TestCapsCachedStartsOneBackgroundProbe(t *testing.T) {
	want := probeRequests(t)
	fake := newCam(t, ptztest.Options{PanTilt: true, Zoom: true, Delay: map[string]time.Duration{"GetCapabilities": 300 * time.Millisecond}})
	m := newManager(t, time.Second)
	cam := camFor(fake)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if _, ok := m.CapsCached(cam); ok {
				t.Error("CapsCached ok=true while the first probe is still running")
			}
			if d := time.Since(start); d > 100*time.Millisecond {
				t.Errorf("CapsCached blocked for %v", d)
			}
		}()
	}
	wg.Wait()
	eventually(t, "background probe to finish", func() bool {
		_, ok := m.CapsCached(cam)
		return ok
	})
	if got := fake.Requests(); got != want {
		t.Errorf("requests = %d, want %d (a single probe)", got, want)
	}
}

func TestCapsSharesInFlightProbe(t *testing.T) {
	want := probeRequests(t)
	fake := newCam(t, ptztest.Options{PanTilt: true, Zoom: true, Delay: map[string]time.Duration{"GetCapabilities": 200 * time.Millisecond}})
	m := newManager(t, time.Second)
	cam := camFor(fake)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			caps, err := m.Caps(context.Background(), cam)
			if err != nil || caps != (Caps{PanTilt: true, Zoom: true}) {
				t.Errorf("Caps = %+v, %v", caps, err)
			}
		}()
	}
	wg.Wait()
	if got := fake.Requests(); got != want {
		t.Errorf("requests = %d, want %d (a single shared probe)", got, want)
	}
}

func TestCallerCancelIsNotCachedAsFailure(t *testing.T) {
	fake := newCam(t, ptztest.Options{PanTilt: true, Zoom: true, Delay: map[string]time.Duration{"GetCapabilities": 300 * time.Millisecond}})
	m := newManager(t, time.Second)
	cam := camFor(fake)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := m.Caps(ctx, cam); !errors.Is(err, context.Canceled) {
		t.Fatalf("Caps err = %v, want context.Canceled", err)
	}
	// Nothing was learned about the camera, so the next caller gets a real answer.
	caps, err := m.Caps(context.Background(), cam)
	if err != nil || caps != (Caps{PanTilt: true, Zoom: true}) {
		t.Errorf("Caps after cancelled caller = %+v, %v", caps, err)
	}
}

func TestResetDropsCache(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, time.Second)
	cam := camFor(fake)
	if _, err := m.Caps(context.Background(), cam); err != nil {
		t.Fatal(err)
	}
	n := fake.Requests()
	if _, err := m.Caps(context.Background(), cam); err != nil || fake.Requests() != n {
		t.Fatalf("second Caps should be cached (requests %d -> %d, err %v)", n, fake.Requests(), err)
	}
	m.Reset()
	if _, ok := m.CapsCached(cam); ok {
		t.Error("CapsCached ok=true right after Reset")
	}
	if _, err := m.Caps(context.Background(), cam); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests(); got <= n {
		t.Errorf("requests = %d after Reset, want more than %d", got, n)
	}
}

func TestChangedTargetRedials(t *testing.T) {
	a := newCam(t, ptztest.Options{PanTilt: true})
	b := newCam(t, ptztest.Options{Zoom: true})
	m := newManager(t, time.Second)
	cam := camFor(a)
	if caps, err := m.Caps(context.Background(), cam); err != nil || caps != (Caps{PanTilt: true}) {
		t.Fatalf("Caps = %+v, %v", caps, err)
	}
	cam.OnvifURL = b.URL // same id, new onvif_url
	if caps, err := m.Caps(context.Background(), cam); err != nil || caps != (Caps{Zoom: true}) {
		t.Errorf("Caps after target change = %+v, %v; want zoom-only from the new camera", caps, err)
	}
	if b.Requests() == 0 {
		t.Error("the new target was never contacted")
	}
}

func TestResetStopsMovingCamera(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, 600*time.Millisecond)
	if err := m.Move(context.Background(), camFor(fake), Left); err != nil {
		t.Fatal(err)
	}
	m.Reset()
	eventually(t, "Stop after Reset", func() bool { return fake.Stops() >= 1 })
	time.Sleep(1200 * time.Millisecond)
	if got := fake.Stops(); got != 1 {
		t.Errorf("Stops = %d, want exactly 1 (the cancelled timer must not fire again)", got)
	}
}

func TestResetConcurrentWithCommands(t *testing.T) {
	fake := ptzFake(t)
	m := newManager(t, 10*time.Millisecond)
	cam := camFor(fake)
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				switch (g + i) % 4 {
				case 0, 1:
					_ = m.Move(context.Background(), cam, Direction([]string{"left", "zoom_in"}[i%2]))
				case 2:
					_ = m.Stop(context.Background(), cam)
				default:
					m.Reset()
				}
			}
		}()
	}
	wg.Wait()
	// The cleanup in newManager verifies nothing is left running; also check
	// the camera was not left with an outstanding move: every Move is
	// eventually answered by at least one Stop.
	m.Reset()
	eventually(t, "a Stop", func() bool { return fake.Stops() > 0 })
	if got := fake.MaxConcurrent(); got != 1 {
		t.Errorf("MaxConcurrent = %d, want 1", got)
	}
}

// flakyStopProxy forwards to the fake but fails the next failStops Stop
// requests with an HTTP 500, as a lossy network would.
func flakyStopProxy(t *testing.T, fake *ptztest.Camera, failStops int64) (config.Camera, *atomic.Int64) {
	t.Helper()
	target, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	var left atomic.Int64
	left.Store(failStops)
	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("<tptz:Stop")) && left.Add(-1) >= 0 {
			http.Error(w, "dropped", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return config.Camera{ID: "c1", SourceMain: "rtsp://u:p@127.0.0.1/x", OnvifURL: srv.URL + target.Path}, &left
}

func TestSafetyStopIsRetriedWhenLost(t *testing.T) {
	fake := ptzFake(t)
	cam, _ := flakyStopProxy(t, fake, 2)
	m := newManager(t, 100*time.Millisecond)
	if err := m.Move(context.Background(), cam, Left); err != nil {
		t.Fatalf("Move: %v", err)
	}
	eventually(t, "safety stop delivered despite two lost attempts", func() bool { return fake.Stops() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if got := fake.Stops(); got != 1 {
		t.Errorf("Stops = %d, want 1", got)
	}
}

func TestFailedUserStopIsRetried(t *testing.T) {
	fake := ptzFake(t)
	cam, _ := flakyStopProxy(t, fake, 1)
	m := newManager(t, 5*time.Second)
	if err := m.Move(context.Background(), cam, Left); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if err := m.Stop(context.Background(), cam); err == nil {
		t.Fatal("Stop err = nil, want the delivery failure")
	}
	// The user-visible Stop failed after cancelling the safety timer; the
	// manager must not leave the camera moving.
	eventually(t, "retried Stop", func() bool { return fake.Stops() >= 1 })
}

// A command on a retired entry and one on its replacement are for the same
// camera and must not overlap, and the retired entry's stale Stop must not land
// after the fresh Move. Deterministic: the slow ContinuousMove keeps the old
// command in flight while the new Move is issued.
func TestCommandsSerialisedAcrossReset(t *testing.T) {
	fake := newCam(t, ptztest.Options{PanTilt: true, Zoom: true, Delay: map[string]time.Duration{"ContinuousMove": 400 * time.Millisecond}})
	m := newManager(t, 5*time.Second)
	cam := camFor(fake)
	if _, err := m.Caps(context.Background(), cam); err != nil {
		t.Fatal(err)
	}
	probeReqs := fake.Requests()

	move1 := make(chan error, 1)
	go func() { move1 <- m.Move(context.Background(), cam, Left) }()
	eventually(t, "first ContinuousMove to reach the camera", func() bool { return fake.Requests() > probeReqs })

	m.Reset() // retires the entry whose Move is still in flight
	if err := m.Move(context.Background(), cam, Right); err != nil {
		t.Fatalf("Move after Reset: %v", err)
	}
	if err := <-move1; err != nil {
		t.Fatalf("first Move: %v", err)
	}
	if got := fake.MaxConcurrent(); got != 1 {
		t.Errorf("MaxConcurrent = %d across Reset, want 1", got)
	}
	mv := fake.Moves()
	if len(mv) != 2 || mv[0].Pan >= 0 || mv[1].Pan <= 0 {
		t.Fatalf("Moves = %+v, want left then right", mv)
	}
	// The fresh Move has been accepted. The only Stop allowed is the old
	// entry's, which was sent before it (the lock ordered it first); nothing
	// may stop the camera afterwards (safety timer is 5 s away).
	stopsAtAccept := fake.Stops()
	time.Sleep(500 * time.Millisecond)
	if got := fake.Stops(); got != stopsAtAccept || got != 1 {
		t.Errorf("Stops = %d (was %d when the fresh Move was accepted), want 1 and unchanged", got, stopsAtAccept)
	}
}
