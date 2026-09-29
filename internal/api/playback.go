// playback.go — UI-PLAN B4: proxy MediaMTX's playback server so the UI can
// list recordings and pull fMP4 windows for the timeline scrubber. The
// browser authenticates to nvrd; nvrd fetches from MediaMTX on localhost.
package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) playbackProxy(w http.ResponseWriter, r *http.Request, subpath string, query url.Values) {
	cfg := s.cfg.Load()
	target := "http://127.0.0.1" + portOf(cfg.Media.PlaybackAddress) +
		"/playback/" + url.PathEscape(subpath) + "?" + query.Encode()
	resp, err := http.Get(target)
	if err != nil {
		http.Error(w, "playback unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// GET /api/playback/{camera}/list?start=RFC3339 — recordings timespans.
func (s *Server) handlePlaybackList(w http.ResponseWriter, r *http.Request) {
	cam := r.PathValue("camera")
	q := url.Values{}
	if start := r.URL.Query().Get("start"); start != "" {
		q.Set("start", start)
	}
	s.playbackProxy(w, r, cam+"/list", q)
}

// GET /api/playback/{camera}/get?start=RFC3339&duration=30s — fMP4 window.
func (s *Server) handlePlaybackGet(w http.ResponseWriter, r *http.Request) {
	cam := r.PathValue("camera")
	q := url.Values{}
	q.Set("start", r.URL.Query().Get("start"))
	if dur := r.URL.Query().Get("duration"); dur != "" {
		q.Set("duration", dur)
	} else {
		q.Set("duration", "30s")
	}
	s.playbackProxy(w, r, cam+"/get", q)
}

// RFC3339 timestamp for playback queries (UI-PLAN U8).
func playbackTime(tsMS int64) string {
	return time.UnixMilli(tsMS).UTC().Format("2006-01-02T15:04:05Z07:00")
}

var _ = strings.TrimSpace
