package api

import (
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// DetectorGauges is the latest heartbeat from COMP-3 (FR-DET-12, FR-OBS-2).
type DetectorGauges struct {
	Backend    string               `json:"backend"`
	InferMs    float64              `json:"inference_ms"`
	QueueDepth int                  `json:"queue_depth"`
	Dropped    uint64               `json:"dropped_frames"`
	FpsScale   float64              `json:"fps_scale"` // thermal governor state
	TempC      float64              `json:"temp_c"`
	Cameras    map[string]CamGauges `json:"cameras"`
}

type CamGauges struct {
	Fps       float64 `json:"fps"`        // effective forwarded fps
	GatePass  uint64  `json:"gate_pass"`  // frames forwarded to inference
	GateTotal uint64  `json:"gate_total"` // frames seen by the gate
}

const detectorStaleAfter = 45 * time.Second

func (s *Server) storeGauges(g *DetectorGauges) {
	s.gauges.Store(g)
	s.gaugesAt.Store(time.Now().Unix())
}

// detectorStatus for /api/health components.
func (s *Server) detectorStatus() string {
	at := s.gaugesAt.Load()
	if at == 0 {
		return "unknown"
	}
	if time.Since(time.Unix(at, 0)) > detectorStaleAfter {
		return "stale"
	}
	return "up"
}

// handleDetectorMetrics ingests the detector heartbeat (localhost only).
func (s *Server) handleDetectorMetrics(w http.ResponseWriter, r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || host != "127.0.0.1" {
		http.Error(w, "internal endpoint", http.StatusForbidden)
		return
	}
	var g DetectorGauges
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil || g.Backend == "" {
		http.Error(w, "bad gauges body", 400)
		return
	}
	s.storeGauges(&g)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// gaugesForMetrics returns the current gauges (or nil).
func (s *Server) gaugesForMetrics() *DetectorGauges {
	return s.gauges.Load()
}
