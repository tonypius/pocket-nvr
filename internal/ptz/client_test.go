package ptz

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pocketnvr/internal/ptz/ptztest"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dial(t *testing.T, cam *ptztest.Camera) *Client {
	t.Helper()
	c, err := Dial(testCtx(t), Target{Endpoints: []string{cam.URL}, User: "u", Pass: "p"}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

func newCam(t *testing.T, o ptztest.Options) *ptztest.Camera {
	t.Helper()
	o.User, o.Pass = "u", "p"
	return ptztest.New(t, o)
}

func TestDialPanTiltZoom(t *testing.T) {
	c := dial(t, newCam(t, ptztest.Options{PanTilt: true, Zoom: true}))
	if got := c.Capabilities(); got != (Caps{PanTilt: true, Zoom: true}) {
		t.Errorf("Capabilities = %+v", got)
	}
}

func TestCapsPanTiltOnly(t *testing.T) {
	c := dial(t, newCam(t, ptztest.Options{PanTilt: true}))
	if got := c.Capabilities(); got != (Caps{PanTilt: true}) {
		t.Errorf("Capabilities = %+v", got)
	}
}

func TestCapsZoomOnly(t *testing.T) {
	c := dial(t, newCam(t, ptztest.Options{Zoom: true}))
	if got := c.Capabilities(); got != (Caps{Zoom: true}) {
		t.Errorf("Capabilities = %+v", got)
	}
}

func TestNoPTZServiceGivesEmptyCaps(t *testing.T) {
	c, err := Dial(testCtx(t), Target{Endpoints: []string{newCam(t, ptztest.Options{NoPTZService: true}).URL}, User: "u", Pass: "p"}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := c.Capabilities(); got != (Caps{}) {
		t.Errorf("Capabilities = %+v, want empty", got)
	}
	if err := c.Move(testCtx(t), Up, time.Second); err == nil {
		t.Error("Move on a camera without PTZ should fail")
	}
}

func TestDialFallsBackToSecondEndpoint(t *testing.T) {
	dead := httptest.NewServer(nil)
	deadURL := dead.URL + "/onvif/device_service"
	dead.Close() // closed port: connection refused

	cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
	c, err := Dial(testCtx(t), Target{Endpoints: []string{deadURL, cam.URL}, User: "u", Pass: "p"}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := c.Capabilities(); got != (Caps{PanTilt: true, Zoom: true}) {
		t.Errorf("Capabilities = %+v", got)
	}
}

func TestDialErrorWhenNoEndpointAnswers(t *testing.T) {
	dead := httptest.NewServer(nil)
	deadURL := dead.URL + "/onvif/device_service"
	dead.Close()

	c, err := Dial(testCtx(t), Target{Endpoints: []string{deadURL}, User: "u", Pass: "p"}, nil)
	if err == nil || c != nil {
		t.Fatalf("Dial = %v, %v; want nil client and an error", c, err)
	}
	if _, err := Dial(testCtx(t), Target{User: "u", Pass: "p"}, nil); err == nil {
		t.Error("Dial with no endpoints should fail")
	}
}

func TestDialWrongPasswordFailsWithoutLeakingIt(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true})
	_, err := Dial(testCtx(t), Target{Endpoints: []string{cam.URL}, User: "u", Pass: "hunter2"}, nil)
	if err == nil {
		t.Fatal("Dial with wrong password should fail")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestAdvertisedHostIsRewritten(t *testing.T) {
	// The fake advertises 192.0.2.1 (non-routable) in every XAddr.
	cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
	c := dial(t, cam)
	if err := c.Move(testCtx(t), Left, 3*time.Second); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if n := len(cam.Moves()); n != 1 {
		t.Errorf("Moves = %d, want 1", n)
	}
}

func TestPrettyPrintedXAddrsStillFindPTZ(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true, PrettyXAddrs: true})
	c := dial(t, cam)
	if got := c.Capabilities(); got != (Caps{PanTilt: true, Zoom: true}) {
		t.Fatalf("Capabilities = %+v, want pan/tilt and zoom (XAddr whitespace must be trimmed)", got)
	}
	if err := c.Move(testCtx(t), Left, 3*time.Second); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if n := len(cam.Moves()); n != 1 {
		t.Errorf("Moves = %d, want 1", n)
	}
}

func TestMoveVelocities(t *testing.T) {
	cases := []struct {
		dir             Direction
		pan, tilt, zoom float64
	}{
		{Left, -0.5, 0, 0},
		{Right, 0.5, 0, 0},
		{Down, 0, -0.5, 0},
		{Up, 0, 0.5, 0},
		{ZoomOut, 0, 0, -0.5},
		{ZoomIn, 0, 0, 0.5},
	}
	for _, tc := range cases {
		t.Run(string(tc.dir), func(t *testing.T) {
			cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
			c := dial(t, cam)
			if err := c.Move(testCtx(t), tc.dir, 3*time.Second); err != nil {
				t.Fatalf("Move: %v", err)
			}
			moves := cam.Moves()
			if len(moves) != 1 {
				t.Fatalf("Moves = %d, want 1", len(moves))
			}
			m := moves[0]
			if m.Pan != tc.pan || m.Tilt != tc.tilt || m.Zoom != tc.zoom {
				t.Errorf("move = %+v, want pan=%v tilt=%v zoom=%v", m, tc.pan, tc.tilt, tc.zoom)
			}
			// Only the moved axis is sent: some firmware faults on a Zoom
			// velocity for a node without zoom.
			zoomMove := tc.dir == ZoomIn || tc.dir == ZoomOut
			if m.HasPanTilt == zoomMove || m.HasZoom != zoomMove {
				t.Errorf("move HasPanTilt=%v HasZoom=%v, want only the %s element", m.HasPanTilt, m.HasZoom, map[bool]string{true: "Zoom", false: "PanTilt"}[zoomMove])
			}
			if m.Timeout != "PT3S" {
				t.Errorf("Timeout = %q, want PT3S", m.Timeout)
			}
		})
	}
}

func TestMoveTimeoutFormat(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true})
	c := dial(t, cam)
	if err := c.Move(testCtx(t), Up, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := cam.Moves()[0].Timeout; got != "PT1.5S" {
		t.Errorf("Timeout = %q, want PT1.5S", got)
	}
}

func TestMoveUnknownDirection(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true})
	c := dial(t, cam)
	if err := c.Move(testCtx(t), Direction("sideways"), time.Second); err == nil {
		t.Error("unknown direction should fail")
	}
	if len(cam.Moves()) != 0 {
		t.Error("no request should be sent for an unknown direction")
	}
}

func TestStopSendsStop(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
	c := dial(t, cam)
	if err := c.Stop(testCtx(t)); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := cam.Stops(); got != 1 {
		t.Errorf("Stops = %d, want 1", got)
	}
}

func TestParseDirection(t *testing.T) {
	for _, d := range []Direction{Up, Down, Left, Right, ZoomIn, ZoomOut} {
		got, ok := ParseDirection(string(d))
		if !ok || got != d {
			t.Errorf("ParseDirection(%q) = %q, %v", d, got, ok)
		}
	}
	if _, ok := ParseDirection("diagonal"); ok {
		t.Error("ParseDirection(diagonal) should fail")
	}
	if _, ok := ParseDirection(""); ok {
		t.Error("ParseDirection(\"\") should fail")
	}
}

// silentListener accepts TCP connections and never replies, like a filtered
// port whose peer swallows the request.
func silentListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return "http://" + l.Addr().String() + "/onvif/device_service"
}

func TestDialSilentEndpointDoesNotStarveNextOne(t *testing.T) {
	cam := newCam(t, ptztest.Options{PanTilt: true, Zoom: true})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, Target{Endpoints: []string{silentListener(t), cam.URL}, User: "u", Pass: "p"}, nil)
	if err != nil {
		t.Fatalf("Dial: %v (the first endpoint ate the whole budget)", err)
	}
	if got := c.Capabilities(); got != (Caps{PanTilt: true, Zoom: true}) {
		t.Errorf("Capabilities = %+v", got)
	}
}

func TestDialWithExpiredContextReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := Dial(ctx, Target{Endpoints: []string{silentListener(t), silentListener(t)}, User: "u", Pass: "p"}, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Dial err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Dial took %v with a done context", d)
	}
}

// redirector returns a camera URL that answers every request with a 307 to a
// second server, and the counter of requests that second server received.
func redirector(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "should not be reached", http.StatusTeapot)
	}))
	t.Cleanup(target.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	return redir.URL + "/onvif/device_service", &hits
}

func TestDialDefaultClientDoesNotFollowRedirects(t *testing.T) {
	url, hits := redirector(t)
	c, err := Dial(testCtx(t), Target{Endpoints: []string{url}, User: "u", Pass: "p"}, nil)
	if err == nil || c != nil {
		t.Fatalf("Dial = %v, %v; want nil client and an error", c, err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests; the SOAP POST (with its WSSE digest) must not be bounced", n)
	}
}
