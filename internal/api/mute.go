package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// maxMuteMinutes bounds a timed mute (30 days); use -1 to mute until
// switched back on.
const maxMuteMinutes = 30 * 24 * 60

// SetMute wires the global alert mute: get returns the raw value (0 = off,
// -1 = until switched back on, else epoch ms) and set persists + applies it.
func (s *Server) SetMute(get func() int64, set func(int64) error) {
	s.muteGet = get
	s.muteSet = set
}

func (s *Server) muteView() map[string]any {
	until := int64(0)
	if s.muteGet != nil {
		until = s.muteGet()
	}
	muted := until == -1 || (until > 0 && time.Now().UnixMilli() < until)
	if !muted {
		until = 0 // an expired snooze reads as "not muted"
	}
	return map[string]any{"muted": muted, "until": until}
}

func (s *Server) handleMuteGet(w http.ResponseWriter, r *http.Request) {
	if s.muteGet == nil {
		http.Error(w, "notifications not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.muteView())
}

// handleMutePost: {"minutes": N} mutes for N minutes, -1 until switched back
// on, 0 unmutes. Muted events are still detected and stored; they just don't
// alert on any channel (push, ntfy, Telegram).
func (s *Server) handleMutePost(w http.ResponseWriter, r *http.Request) {
	if s.muteSet == nil {
		http.Error(w, "notifications not available", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		Minutes *int `json:"minutes"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Minutes == nil {
		http.Error(w, `body must be {"minutes": N}`, http.StatusBadRequest)
		return
	}
	m := *in.Minutes
	var until int64
	switch {
	case m == 0:
		until = 0
	case m == -1:
		until = -1
	case m > 0 && m <= maxMuteMinutes:
		until = time.Now().Add(time.Duration(m) * time.Minute).UnixMilli()
	default:
		http.Error(w, "minutes must be 0 (unmute), -1 (until turned on) or 1..43200", http.StatusBadRequest)
		return
	}
	if err := s.muteSet(until); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("notification mute changed", "minutes", m)
	writeJSON(w, http.StatusOK, s.muteView())
}
