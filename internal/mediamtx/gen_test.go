package mediamtx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocketnvr/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfgYAML := `
system:
  base_path: /data/nvr
recording:
  segment_seconds: 300
  retention_days: 7
cameras:
  - id: front_tapo
    name: Front Door
    main_url: rtsp://127.0.0.1:8554/front_tapo
    sub_url: rtsp://127.0.0.1:8554/front_tapo_sub
    source_main: rtsp://u:p@192.168.0.20:554/stream1
    source_sub: rtsp://u:p@192.168.0.20:554/stream2
    enabled: true
  - id: back_cpplus
    name: Back Yard
    main_url: rtsp://127.0.0.1:8554/back_cpplus
    sub_url: rtsp://127.0.0.1:8554/back_cpplus_sub
    source_main: rtsp://u:p@192.168.0.21:554/cam/realmonitor?channel=1&subtype=0
    source_sub: rtsp://u:p@192.168.0.21:554/cam/realmonitor?channel=1&subtype=1
    enabled: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestGenerate(t *testing.T) {
	out, err := Generate(testConfig(t), "viewersecret", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"rtspAddress: :8554",
		"recordFormat: fmp4",
		"recordSegmentDuration: 5m0s",
		"recordDeleteAfter: 168h0m0s",
		"recordPath: /data/nvr/recordings/%path/",
		"pass: viewersecret",
		"ips: [127.0.0.1, '::1']",
		"  front_tapo:",
		"    source: rtsp://u:p@192.168.0.20:554/stream1",
		"  front_tapo_sub:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "back_cpplus") {
		t.Error("disabled camera must not be generated")
	}
}

func TestGenerateEventOnlyDisablesRecord(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
system: {base_path: /data/nvr}
recording: {mode: event_only}
cameras:
  - id: cam1
    name: C1
    main_url: rtsp://127.0.0.1:8554/cam1
    sub_url: rtsp://127.0.0.1:8554/cam1_sub
    source_main: rtsp://u:p@10.0.0.1/main
    source_sub: rtsp://u:p@10.0.0.1/sub
`), 0o600)
	os.WriteFile(filepath.Join(dir, "secrets.yaml"), nil, 0o600)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(cfg, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "record: no") {
		t.Errorf("event_only must set record: no:\n%s", out)
	}
}
