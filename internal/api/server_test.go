package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"pocketnvr/internal/config"
	"pocketnvr/internal/store"
)

func setup(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
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
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0o600)
	os.WriteFile(filepath.Join(dir, "secrets.yaml"),
		[]byte("api_token: sekrit\ncam_user: camadmin\ncam_pass: Sup3r$ecret\n"), 0o600)
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
	}{{"cam1", "C1", true}})

	templates := map[string]string{
		"Cameras.0.SourceMain": "rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/main",
		"Cameras.0.SourceSub":  "rtsp://${secret:cam_user}:${secret:cam_pass}@10.0.0.1/sub",
	}
	srv := New(cfg, config.NewRedactorFromTemplates(templates), "sekrit", st, slog.Default(), dir)
	srv.SetDirs(dir)
	hups := 0
	srv.SetHupSelf(func() { hups++ })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func get(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestAuthRequired(t *testing.T) {
	_, ts := setup(t)
	resp, _ := get(t, ts.URL+"/api/health", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp, _ = get(t, ts.URL+"/api/health?token=sekrit", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("?token= should work, got %d", resp.StatusCode)
	}
}

func TestHealthAndMetrics(t *testing.T) {
	_, ts := setup(t)
	resp, body := get(t, ts.URL+"/api/health", "sekrit")
	if resp.StatusCode != 200 {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	for _, want := range []string{`"status": "ok"`, `"nvrd": "up"`, "uptime_s"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("health missing %q: %s", want, body)
		}
	}
	resp, body = get(t, ts.URL+"/api/metrics", "sekrit")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "events_total") {
		t.Fatalf("metrics: %d %s", resp.StatusCode, body)
	}
}

func TestConfigRedacted(t *testing.T) {
	_, ts := setup(t)
	_, body := get(t, ts.URL+"/api/config", "sekrit")
	// The source URL creds came from secrets.yaml; /api/config must show
	// placeholders, never the values (FR-CFG-2).
	if strings.Contains(string(body), "camadmin") || strings.Contains(string(body), "Sup3r") {
		t.Errorf("secret leaked in /api/config: %s", body)
	}
	if !strings.Contains(string(body), "${secret:cam_user}") {
		t.Errorf("expected placeholder in redacted config: %s", body)
	}
}

func TestEventFlow(t *testing.T) {
	_, ts := setup(t)
	// detector ingest (from localhost)
	end := time.Now().UnixMilli() + 5000
	body := `{"camera_id":"cam1","label":"person","score":0.91,"start_ts":` +
		jsonInt64(time.Now().UnixMilli()) + `,"end_ts":` + jsonInt64(end) + `,"bbox":[412,220,96,210]}`
	resp, err := http.Post(ts.URL+"/api/internal/events?token=sekrit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("internal event post: %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)

	resp, listBody := get(t, ts.URL+"/api/events?camera=cam1&limit=10", "sekrit")
	if resp.StatusCode != 200 || !strings.Contains(string(listBody), `"person"`) {
		t.Fatalf("events list: %d %s", resp.StatusCode, listBody)
	}
	resp, detail := get(t, ts.URL+fmt.Sprintf("/api/events/%d", created.ID), "sekrit")
	if resp.StatusCode != 200 || !strings.Contains(string(detail), "0.91") {
		t.Fatalf("event detail: %d %s", resp.StatusCode, detail)
	}
	// missing asset → 404
	resp, _ = get(t, ts.URL+fmt.Sprintf("/api/events/%d/snapshot", created.ID), "sekrit")
	if resp.StatusCode != 404 {
		t.Fatalf("snapshot without asset should 404, got %d", resp.StatusCode)
	}
}

// B1 regression: /crop must serve the stored crop, not fall back to the clip.
func TestEventCropAssetServed(t *testing.T) {
	_, ts := setup(t)
	snap := []byte("\xff\xd8\xff\xe0fake-snapshot-jpeg")
	crop := []byte("\xff\xd8\xff\xe0fake-crop-jpeg")
	body := `{"camera_id":"cam1","label":"person","score":0.8,"start_ts":` +
		jsonInt64(time.Now().UnixMilli()) +
		`,"snapshot_jpeg_b64":"` + base64.StdEncoding.EncodeToString(snap) +
		`","crop_jpeg_b64":"` + base64.StdEncoding.EncodeToString(crop) + `"}`

	resp, err := http.Post(ts.URL+"/api/internal/events?token=sekrit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("internal event post: %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)

	r, got := get(t, ts.URL+fmt.Sprintf("/api/events/%d/crop", created.ID), "sekrit")
	if r.StatusCode != 200 {
		t.Fatalf("crop: %d (%s)", r.StatusCode, got)
	}
	if r.Header.Get("Content-Type") != "image/jpeg" || string(got) != string(crop) {
		t.Fatalf("crop asset mismatch: type=%s body=%q", r.Header.Get("Content-Type"), got)
	}
	r, got = get(t, ts.URL+fmt.Sprintf("/api/events/%d/snapshot", created.ID), "sekrit")
	if r.StatusCode != 200 || string(got) != string(snap) {
		t.Fatalf("snapshot asset mismatch: %d %q", r.StatusCode, got)
	}
}

func TestLiveRedirectUnknownCamera(t *testing.T) {
	_, ts := setup(t)
	resp, _ := get(t, ts.URL+"/api/live/nope", "sekrit")
	if resp.StatusCode != 404 {
		t.Fatalf("unknown camera live: %d", resp.StatusCode)
	}
}

func jsonInt64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestConfigPutRoundTrip(t *testing.T) {
	srv, ts := setup(t)
	_ = srv
	// GET returns the redacted config; PUT-ing it back must be accepted
	// (placeholders re-resolve server-side) and written to disk.
	_, cfgBody := get(t, ts.URL+"/api/config", "sekrit")
	resp, err := http.NewRequest("PUT", ts.URL+"/api/config", strings.NewReader(string(cfgBody)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Set("X-Api-Token", "sekrit")
	resp.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", r2.StatusCode, readAll(r2))
	}
	r2.Body.Close()
}

func TestConfigPutRejectsInvalid(t *testing.T) {
	_, ts := setup(t)
	bad := `{"System":{"BasePath":"/x"},"Cameras":[{"ID":"ok","SourceMain":"not-a-url"}]}`
	req, _ := http.NewRequest("PUT", ts.URL+"/api/config", strings.NewReader(bad))
	req.Header.Set("X-Api-Token", "sekrit")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid config should 400, got %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSecretsPost(t *testing.T) {
	_, ts := setup(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/secrets", strings.NewReader(`{"front_user":"bob"}`))
	req.Header.Set("X-Api-Token", "sekrit")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 {
		t.Fatalf("secrets post: %d", r.StatusCode)
	}
	// secret must NOT be retrievable via GET /api/config
	_, body := get(t, ts.URL+"/api/config", "sekrit")
	if strings.Contains(string(body), "bob") {
		t.Error("secret leaked via /api/config")
	}
}

func readAll(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// Regression: two cameras sharing the same secret VALUES must each get
// their own placeholder back (path-based redaction, FLAG-13).
func TestRedactionPathNotValueBased(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
cameras:
  - id: a
    name: A
    source_main: rtsp://${secret:user}:${secret:pass}@10.0.0.1/main
    source_sub: rtsp://${secret:user}:${secret:pass}@10.0.0.1/sub
  - id: b
    name: B
    source_main: rtsp://${secret:user}:${secret:pass}@10.0.0.2/main
    source_sub: rtsp://${secret:user}:${secret:pass}@10.0.0.2/sub
`), 0o600)
	os.WriteFile(filepath.Join(dir, "secrets.yaml"), []byte("user: shared\npass: alsoname\n"), 0o600)
	_, templates, err := config.LoadEx(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := config.NewRedactorFromTemplates(templates)
	red := r.RedactedCopy(loadedCfg(t, dir))
	m, _ := json.Marshal(red)
	sb := string(m)
	if strings.Count(sb, "${secret:user}") != 4 {
		t.Errorf("want 4 user placeholders (2 cams × main+sub), got %d in %s",
			strings.Count(sb, "${secret:user}"), sb)
	}
	if strings.Contains(sb, "shared") {
		t.Error("secret value leaked")
	}
}

func loadedCfg(t *testing.T, dir string) *config.Config {
	t.Helper()
	c, _, err := config.LoadEx(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
