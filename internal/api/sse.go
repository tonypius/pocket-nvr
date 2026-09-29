// detector.go additions — SSE live event push (B3) so the Events rail and
// Detections page update the moment an event lands.
package api

import (
	"fmt"
	"net/http"
)

// subscribe via GET /api/events/stream (text/event-stream). Clients get a
// `event` JSON per new event plus a `hello` comment; auto-reconnect is the
// browser EventSource default.
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	fmt.Fprintf(w, ": hello\n\n")
	fl.Flush()

	ch := make(chan []byte, 16)
	s.addSSEClient(ch)
	defer s.removeSSEClient(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		}
	}
}

// publishEvent broadcasts one event JSON to all SSE clients.
func (s *Server) publishEvent(evJSON []byte) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for _, ch := range s.sseClients {
		select {
		case ch <- evJSON:
		default: // slow client: drop rather than block the ingest path
		}
	}
}
