package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocketnvr/internal/config"
	"pocketnvr/internal/ptz/ptztest"
	"pocketnvr/internal/store"
)

const (
	ptzCamUser = "camadmin"
	ptzCamPass = "Sup3r$ecret"
)

// deadONVIFURL returns a device-service URL on a port nothing listens on.
func deadONVIFURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr + "/onvif/device_service"
}

// silentONVIFURL returns a device-service URL whose server accepts
// connections and never replies, like a filtered or hung camera. The listener
// is closed on cleanup.
func silentONVIFURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return "http://" + l.Addr().String() + "/onvif/device_service"
}

// setupPTZ builds a server whose camera "cam1" is the fake ONVIF camera and
// whose camera "dead" has an ONVIF endpoint nobody answers on.
func setupPTZ(t *testing.T, o ptztest.Options) (*Server, *httptest.Server, *ptztest.Camera) {
	t.Helper()
	return setupPTZDead(t, o, deadONVIFURL(t))
}

// setupPTZDead is setupPTZ with the "dead" camera's onvif_url given.
func setupPTZDead(t *testing.T, o ptztest.Options, deadURL string) (*Server, *httptest.Server, *ptztest.Camera) {
	t.Helper()
	o.User, o.Pass = ptzCamUser, ptzCamPass
	fake := ptztest.New(t, o)
	dir := t.TempDir()
	cfgYAML := `
system: {base_path: ` + filepath.Join(dir, "data") + `}
cameras:
  - id: cam1
    name: C1
    main_url: rtsp://127.0.0.1:8554/cam1
    sub_url: rtsp://127.0.0.1:8554/cam1_sub
    source_main: rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/main
    source_sub: rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/sub
    onvif_url: ` + fake.URL + `
  - id: dead
    name: Dead
    main_url: rtsp://127.0.0.1:8554/dead
    sub_url: rtsp://127.0.0.1:8554/dead_sub
    source_main: rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.2/main
    source_sub: rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.2/sub
    onvif_url: ` + deadURL + `
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0o600)
	os.WriteFile(filepath.Join(dir, "secrets.yaml"),
		[]byte("api_token: sekrit\ncam_user: "+ptzCamUser+"\ncam_pass: "+ptzCamPass+"\n"), 0o600)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.System.BasePath, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SeedCameras([]struct {
		ID      string
		Name    string
		Enabled bool
	}{{"cam1", "C1", true}, {"dead", "Dead", true}})

	templates := map[string]string{
		"Cameras.0.SourceMain": "rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/main",
		"Cameras.0.SourceSub":  "rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/sub",
	}
	srv := New(cfg, config.NewRedactorFromTemplates(templates), "sekrit", st, slog.Default(), dir)
	srv.SetDirs(dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(srv.ptz.Reset) // runs before ts.Close: cancels background probes
	return srv, ts, fake
}

func post(t *testing.T, url, token, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestPTZRoutesRequireAuth(t *testing.T) {
	_, ts, _ := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	if r, _ := get(t, ts.URL+"/api/cameras/cam1/ptz", ""); r.StatusCode != 401 {
		t.Errorf("GET ptz = %d, want 401", r.StatusCode)
	}
	if r, _ := post(t, ts.URL+"/api/cameras/cam1/ptz/move", "", `{"dir":"left"}`); r.StatusCode != 401 {
		t.Errorf("POST move = %d, want 401", r.StatusCode)
	}
	if r, _ := post(t, ts.URL+"/api/cameras/cam1/ptz/stop", "", ""); r.StatusCode != 401 {
		t.Errorf("POST stop = %d, want 401", r.StatusCode)
	}
}

func TestPTZCapsEndpoint(t *testing.T) {
	_, ts, _ := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	resp, body := get(t, ts.URL+"/api/cameras/cam1/ptz", "sekrit")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var got map[string]bool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["pan_tilt"] || !got["zoom"] {
		t.Errorf("caps = %s, want pan_tilt and zoom true", body)
	}
}

func TestPTZCapsDeadCameraIs200AllFalse(t *testing.T) {
	_, ts, _ := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	resp, body := get(t, ts.URL+"/api/cameras/dead/ptz", "sekrit")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var got map[string]bool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["pan_tilt"]; !ok || v {
		t.Errorf("caps = %s, want pan_tilt false", body)
	}
	if v, ok := got["zoom"]; !ok || v {
		t.Errorf("caps = %s, want zoom false", body)
	}
}

func TestPTZMoveAndStop(t *testing.T) {
	_, ts, fake := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	resp, body := post(t, ts.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"left"}`)
	if resp.StatusCode != 204 {
		t.Fatalf("move status = %d: %s", resp.StatusCode, body)
	}
	moves := fake.Moves()
	if len(moves) == 0 || moves[0].Pan != -0.5 {
		t.Fatalf("moves = %+v, want first Pan == -0.5", moves)
	}
	resp, body = post(t, ts.URL+"/api/cameras/cam1/ptz/stop", "sekrit", "")
	if resp.StatusCode != 204 {
		t.Fatalf("stop status = %d: %s", resp.StatusCode, body)
	}
	if fake.Stops() < 1 {
		t.Errorf("Stops() = %d, want >= 1", fake.Stops())
	}
}

func TestPTZErrors(t *testing.T) {
	_, ts, _ := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	move := func(id, body string) int {
		r, _ := post(t, ts.URL+"/api/cameras/"+id+"/ptz/move", "sekrit", body)
		return r.StatusCode
	}
	if c := move("nope", `{"dir":"left"}`); c != 404 {
		t.Errorf("unknown camera move = %d, want 404", c)
	}
	if r, _ := get(t, ts.URL+"/api/cameras/nope/ptz", "sekrit"); r.StatusCode != 404 {
		t.Errorf("unknown camera caps = %d, want 404", r.StatusCode)
	}
	if r, _ := post(t, ts.URL+"/api/cameras/nope/ptz/stop", "sekrit", ""); r.StatusCode != 404 {
		t.Errorf("unknown camera stop = %d, want 404", r.StatusCode)
	}
	if c := move("cam1", `{"dir":"sideways"}`); c != 400 {
		t.Errorf("unknown dir = %d, want 400", c)
	}
	if c := move("cam1", `not json`); c != 400 {
		t.Errorf("bad JSON = %d, want 400", c)
	}
	if c := move("dead", `{"dir":"left"}`); c != 502 {
		t.Errorf("dead camera move = %d, want 502", c)
	}
	if r, _ := post(t, ts.URL+"/api/cameras/dead/ptz/stop", "sekrit", ""); r.StatusCode != 502 {
		t.Errorf("dead camera stop = %d, want 502", r.StatusCode)
	}

	_, ts2, _ := setupPTZ(t, ptztest.Options{NoPTZService: true})
	if r, _ := post(t, ts2.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"left"}`); r.StatusCode != 409 {
		t.Errorf("non-PTZ move = %d, want 409", r.StatusCode)
	}
	if r, _ := post(t, ts2.URL+"/api/cameras/cam1/ptz/stop", "sekrit", ""); r.StatusCode != 409 {
		t.Errorf("non-PTZ stop = %d, want 409", r.StatusCode)
	}

	// Pan/tilt only: zoom is an unsupported movement, also a 409.
	_, ts3, _ := setupPTZ(t, ptztest.Options{PanTilt: true})
	if r, _ := post(t, ts3.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"zoom_in"}`); r.StatusCode != 409 {
		t.Errorf("unsupported zoom = %d, want 409", r.StatusCode)
	}
}

func TestCamerasListPTZFieldIsCacheOnly(t *testing.T) {
	// The dead camera accepts connections and never answers, so a handler
	// that waited on its probe would block for the manager's whole 3 s probe
	// timeout (a refused connection would fail instantly and prove nothing).
	_, ts, _ := setupPTZDead(t, ptztest.Options{PanTilt: true, Zoom: true}, silentONVIFURL(t))
	cams := func() map[string]map[string]any {
		t.Helper()
		start := time.Now()
		resp, body := get(t, ts.URL+"/api/cameras", "sekrit")
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("GET /api/cameras took %v, must not wait on cameras", d)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		var list []map[string]any
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		m := map[string]map[string]any{}
		for _, c := range list {
			m[c["id"].(string)] = c
		}
		return m
	}
	if _, has := cams()["dead"]["ptz"]; has {
		t.Error("dead camera must not have a ptz key")
	}
	if r, _ := get(t, ts.URL+"/api/cameras/cam1/ptz", "sekrit"); r.StatusCode != 200 {
		t.Fatalf("caps status = %d", r.StatusCode)
	}
	m := cams()
	p, ok := m["cam1"]["ptz"].(map[string]any)
	if !ok || p["pan_tilt"] != true {
		t.Errorf("cam1 ptz = %v, want pan_tilt true", m["cam1"]["ptz"])
	}
	if _, has := m["dead"]["ptz"]; has {
		t.Error("dead camera must still have no ptz key")
	}
}

const deadURLPass = "L3akyD34dP4ss"

func TestPTZNeverLeaksCredentials(t *testing.T) {
	// The dead camera's onvif_url itself carries user:password.
	deadURL := strings.Replace(deadONVIFURL(t), "http://", "http://deaduser:"+deadURLPass+"@", 1)
	_, ts, _ := setupPTZDead(t, ptztest.Options{PanTilt: true}, deadURL)
	_, ts2, _ := setupPTZ(t, ptztest.Options{NoPTZService: true})

	var all []string
	collect := func(_ *http.Response, b []byte) { all = append(all, string(b)) }
	for _, id := range []string{"cam1", "dead", "nope"} {
		collect(get(t, ts.URL+"/api/cameras/"+id+"/ptz", "sekrit"))
		collect(post(t, ts.URL+"/api/cameras/"+id+"/ptz/move", "sekrit", `{"dir":"left"}`))
		collect(post(t, ts.URL+"/api/cameras/"+id+"/ptz/move", "sekrit", `{"dir":"sideways"}`))
		collect(post(t, ts.URL+"/api/cameras/"+id+"/ptz/stop", "sekrit", ""))
	}
	collect(get(t, ts.URL+"/api/cameras", "sekrit"))

	// Exact bodies for each error class.
	body := func(want int, wantBody string, resp *http.Response, b []byte) {
		t.Helper()
		collect(resp, b)
		if resp.StatusCode != want {
			t.Errorf("status = %d, want %d (%s)", resp.StatusCode, want, b)
		}
		if got := strings.TrimSpace(string(b)); got != wantBody {
			t.Errorf("%d body = %q, want exactly %q", want, got, wantBody)
		}
	}
	r, b := post(t, ts.URL+"/api/cameras/nope/ptz/move", "sekrit", `{"dir":"left"}`)
	body(404, "unknown camera", r, b)
	r, b = post(t, ts.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"sideways"}`)
	body(400, "unknown direction", r, b)
	r, b = post(t, ts.URL+"/api/cameras/cam1/ptz/move", "sekrit", `not json`)
	body(400, "invalid JSON body", r, b)
	r, b = post(t, ts.URL+"/api/cameras/dead/ptz/move", "sekrit", `{"dir":"left"}`)
	body(502, "camera did not respond", r, b)
	r, b = post(t, ts.URL+"/api/cameras/dead/ptz/stop", "sekrit", "")
	body(502, "camera did not respond", r, b)
	r, b = post(t, ts2.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"left"}`)
	body(409, "camera has no PTZ", r, b)
	r, b = post(t, ts.URL+"/api/cameras/cam1/ptz/move", "sekrit", `{"dir":"zoom_in"}`)
	body(409, "camera does not support that movement", r, b)

	for _, b := range all {
		for _, secret := range []string{ptzCamPass, "Sup3r%24ecret", ptzCamUser, deadURLPass, "deaduser"} {
			if strings.Contains(b, secret) {
				t.Errorf("response leaks %q: %s", secret, b)
			}
		}
	}
}

func TestSwapConfigResetsPTZ(t *testing.T) {
	srv, ts, fake := setupPTZ(t, ptztest.Options{PanTilt: true, Zoom: true})
	if r, b := get(t, ts.URL+"/api/cameras/cam1/ptz", "sekrit"); r.StatusCode != 200 {
		t.Fatalf("caps status = %d: %s", r.StatusCode, b)
	}
	probed := fake.Requests()
	if probed == 0 {
		t.Fatal("expected the first lookup to probe the fake")
	}
	// Cached: a second lookup makes no request.
	get(t, ts.URL+"/api/cameras/cam1/ptz", "sekrit")
	if got := fake.Requests(); got != probed {
		t.Fatalf("requests %d -> %d: capabilities were not cached", probed, got)
	}

	// Reload with an identical config: only Reset (not a changed target) can
	// make the camera be probed again.
	srv.SwapConfig(srv.cfg.Load(), srv.redact.Load())
	if r, b := get(t, ts.URL+"/api/cameras/cam1/ptz", "sekrit"); r.StatusCode != 200 {
		t.Fatalf("caps status after swap = %d: %s", r.StatusCode, b)
	}
	if got := fake.Requests(); got <= probed {
		t.Errorf("requests stayed at %d after SwapConfig: the PTZ cache was not reset", got)
	}
}
