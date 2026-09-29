package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, config, secrets string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.yaml"), []byte(secrets), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const validConfig = `
system:
  base_path: /data/nvr
media:
  auth_user: viewer
cameras:
  - id: front_tapo
    name: Front Door
    main_url: rtsp://127.0.0.1:8554/front_tapo
    sub_url: rtsp://127.0.0.1:8554/front_tapo_sub
    source_main: rtsp://${secret:front_user}:${secret:front_pass}@192.168.0.20:554/stream1
    source_sub:  rtsp://${secret:front_user}:${secret:front_pass}@192.168.0.20:554/stream2
    enabled: true
    detect:
      zones:
        - name: driveway
          polygon: [[0,180],[640,180],[640,360],[0,360]]
`

const validSecrets = `front_user: alice
front_pass: p@ss:word/1
mediamtx_viewer_pass: viewersecret
`

func TestLoadValidConfig(t *testing.T) {
	dir := writeFiles(t, validConfig, validSecrets)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cam := cfg.Cameras[0]
	// Creds with URL-special chars must be percent-escaped after interpolation.
	if cam.SourceMain != "rtsp://alice:p%40ss%3Aword%2F1@192.168.0.20:554/stream1" {
		t.Errorf("secret interpolation/escaping wrong: %q", cam.SourceMain)
	}
	if cam.Detect.FPS != 5 || cam.Detect.Threshold != 0.5 || cam.Detect.EnterFrames != 3 {
		t.Errorf("defaults not applied: %+v", cam.Detect)
	}
	if cfg.Recording.Mode != "continuous" || cfg.API.Bind != "0.0.0.0:8099" {
		t.Errorf("section defaults not applied: %+v %+v", cfg.Recording, cfg.API)
	}
}

func TestUnknownSecretFails(t *testing.T) {
	dir := writeFiles(t, validConfig, "front_user: alice\n")
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "front_pass") {
		t.Fatalf("want unknown-secret error, got %v", err)
	}
}

func TestInvalidConfigAggregatesErrors(t *testing.T) {
	bad := `
system:
  base_path: /data/nvr
  log_level: loud
cameras:
  - id: FRONT
    name: x
    main_url: rtsp://127.0.0.1:8554/front
    sub_url: rtsp://127.0.0.1:8554/front_sub
    source_main: http://nope
    source_sub: rtsp://192.168.0.20/stream2
    detect:
      fps: 60
      anchor: middle
recording:
  mode: sometimes
detection:
  backend: tpu
`
	dir := writeFiles(t, bad, validSecrets)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("want validation error")
	}
	for _, want := range []string{"log_level", "id", "source_main", "fps", "anchor", "recording.mode", "detection.backend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestEmptyConfigStillBootsWithDefaults(t *testing.T) {
	// FR-CFG-4: minimal config (cameras only) is valid.
	dir := writeFiles(t, "cameras:\n  - id: cam1\n    name: C1\n    source_main: rtsp://u:p@10.0.0.1/main\n    source_sub: rtsp://u:p@10.0.0.1/sub\n", validSecrets)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("minimal config rejected: %v", err)
	}
	if cfg.Recording.SegmentSeconds != 300 || cfg.Detection.InputSize != 640 {
		t.Errorf("defaults missing: %+v", cfg)
	}
}

func TestRedactor(t *testing.T) {
	dir := writeFiles(t, validConfig, validSecrets)
	cfg, templates, err := LoadEx(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRedactorFromTemplates(templates)
	red := r.RedactedCopy(cfg)
	raw, _ := json.Marshal(red)
	got := string(raw)
	if strings.Contains(got, "alice") || strings.Contains(got, "viewersecret") {
		t.Errorf("secret value leaked: %s", got)
	}
	if !strings.Contains(got, "${secret:front_pass}") {
		t.Errorf("placeholder missing: %s", got)
	}
}

func TestQuietMinutes(t *testing.T) {
	if QuietMinutes("23:00") != 23*60 {
		t.Error("23:00 parse")
	}
	if QuietMinutes("") != -1 {
		t.Error("empty should be -1")
	}
}
