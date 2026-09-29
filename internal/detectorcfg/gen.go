// Package detectorcfg renders the detector daemon's runtime config from
// the single config.yaml (FR-CFG-1). The detector (C++, COMP-3) speaks
// JSON, so nvrd resolves secrets (API token, localhost restream URLs) and
// emits detector.json — written 0600, root-owned on the device.
package detectorcfg

import (
	"encoding/json"
	"fmt"

	"pocketnvr/internal/config"
	"pocketnvr/internal/mediamtx"
)

// Analyze frame dimensions: sub-streams are scaled to this fixed size in
// the capture pipeline; zones in config.yaml are expressed in this space.
const (
	FrameWidth  = 640
	FrameHeight = 360
)

type Zone struct {
	Name    string       `json:"name"`
	Polygon [][2]float64 `json:"polygon"`
}

type Camera struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	SubURL        string   `json:"sub_url"`
	FPS           int      `json:"fps"`
	Threshold     float64  `json:"threshold"`
	EnterFrames   int      `json:"enter_frames"`
	ExitFrames    int      `json:"exit_frames"`
	MotionMinArea float64  `json:"motion_min_area"`
	Anchor        string   `json:"anchor"`
	Zones         []Zone   `json:"zones"`
	Classes       []string `json:"classes"`
}

type Detection struct {
	ModelDir  string `json:"model_dir"`
	Model     string `json:"model"`
	InputSize int    `json:"input_size"`
	Backend   string `json:"backend"` // vulkan (falls back to cpu at runtime)
	QueueMax  int    `json:"queue_max"`
	TempHighC int    `json:"temp_high_c"`
	TempLowC  int    `json:"temp_low_c"`
}

type Config struct {
	API struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	} `json:"api"`
	Detection Detection `json:"detection"`
	Frame     struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"frame"`
	Cameras []Camera `json:"cameras"`
}

// Generate renders detector.json from config + the API token (a secret —
// the caller owns writing it 0600).
func Generate(cfg *config.Config, apiToken, basePath string) ([]byte, error) {
	var out Config
	out.API.URL = "http://127.0.0.1" + portOf(cfg.API.Bind)
	out.API.Token = apiToken
	out.Detection = Detection{
		ModelDir:  basePath + "/models/" + cfg.Detection.Model,
		Model:     cfg.Detection.Model,
		InputSize: cfg.Detection.InputSize,
		Backend:   cfg.Detection.Backend,
		QueueMax:  cfg.Detection.QueueMax,
		TempHighC: cfg.Detection.TempHighC,
		TempLowC:  cfg.Detection.TempLowC,
	}
	out.Frame.Width = FrameWidth
	out.Frame.Height = FrameHeight

	for _, c := range cfg.Cameras {
		if !c.IsEnabled() {
			continue
		}
		_, sub := mediamtx.LocalStreamURLs(cfg, c.ID)
		cam := Camera{
			ID: c.ID, Name: c.Name, Enabled: true,
			SubURL:        sub,
			FPS:           c.Detect.FPS,
			Threshold:     c.Detect.Threshold,
			EnterFrames:   c.Detect.EnterFrames,
			ExitFrames:    c.Detect.ExitFrames,
			MotionMinArea: c.Detect.MotionMinArea,
			Anchor:        c.Detect.Anchor,
			Zones:         []Zone{},
			Classes:       c.Detect.Classes,
		}
		for _, z := range c.Detect.Zones {
			z := Zone{Name: z.Name, Polygon: z.Polygon}
			cam.Zones = append(cam.Zones, z)
		}
		out.Cameras = append(out.Cameras, cam)
	}
	if len(out.Cameras) == 0 {
		return nil, fmt.Errorf("no enabled cameras for detector")
	}
	return json.MarshalIndent(out, "", "  ")
}

// portOf extracts ":8099" from "0.0.0.0:8099".
func portOf(bind string) string {
	for i := 0; i < len(bind); i++ {
		if bind[i] == ':' {
			return bind[i:]
		}
	}
	return ":" + bind
}
