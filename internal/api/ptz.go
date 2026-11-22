package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"pocketnvr/internal/config"
	"pocketnvr/internal/ptz"
)

// cameraByID finds a camera in the live (already interpolated) config. The
// returned Camera carries real credentials and must never reach a response
// or a log line.
func (s *Server) cameraByID(id string) (config.Camera, bool) {
	for _, c := range s.cfg.Load().Cameras {
		if c.ID == id {
			return c, true
		}
	}
	return config.Camera{}, false
}

// handlePTZCaps reports what the camera can do. It blocks for at most the
// manager's probe timeout; an unreachable camera is reported as "no PTZ"
// rather than as an error, so the UI can simply hide its controls.
func (s *Server) handlePTZCaps(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.cameraByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "unknown camera", http.StatusNotFound)
		return
	}
	caps, err := s.ptz.Caps(r.Context(), cam)
	if err != nil {
		s.logger.Debug("ptz caps", "camera", cam.ID, "ok", false)
		caps = ptz.Caps{}
	}
	writeJSON(w, http.StatusOK, caps)
}

// handlePTZMove starts (or renews) a movement; the manager's safety timer
// stops it unless the client keeps repeating the request.
func (s *Server) handlePTZMove(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.cameraByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "unknown camera", http.StatusNotFound)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	dir, ok := ptz.ParseDirection(body.Dir)
	if !ok {
		http.Error(w, "unknown direction", http.StatusBadRequest)
		return
	}
	s.ptzResult(w, cam.ID, "move", s.ptz.Move(r.Context(), cam, dir))
}

// handlePTZStop stops any movement.
func (s *Server) handlePTZStop(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.cameraByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "unknown camera", http.StatusNotFound)
		return
	}
	s.ptzResult(w, cam.ID, "stop", s.ptz.Stop(r.Context(), cam))
}

// ptzResult maps a manager error to a response. Upstream error text is never
// echoed: it can carry camera URLs and hostnames.
func (s *Server) ptzResult(w http.ResponseWriter, id, action string, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ptz.ErrNotPTZ), errors.Is(err, ptz.ErrUnsupported):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		s.logger.Debug("ptz command failed", "camera", id, "action", action)
		http.Error(w, "camera did not respond", http.StatusBadGateway)
	}
}
