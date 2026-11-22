package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pocketnvr/internal/store"
)

// maxPushDevices caps stored subscriptions: this is a single-household
// appliance, and every stored endpoint is a URL nvrd will POST to.
const maxPushDevices = 20

// SetPush wires Web Push: the VAPID public key the browser needs to
// subscribe, and a hook that sends a test notification to every device.
func (s *Server) SetPush(vapidPublic func() string, test func(context.Context) (int, error)) {
	s.pushKey = vapidPublic
	s.pushTest = test
}

// validatePushEndpoint is a variable so tests can allow plain-http loopback
// endpoints. nvrd POSTs to whatever endpoint a token holder registers, so
// refuse anything that is not a public https hostname (no SSRF to the LAN,
// loopback or link-local services).
var validatePushEndpoint = func(raw string) error {
	if len(raw) > 2048 {
		return fmt.Errorf("endpoint too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("endpoint must be an https URL")
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return fmt.Errorf("endpoint host must be a name, not an IP address")
	}
	if !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("endpoint host must be a public hostname")
	}
	return nil
}

type pushSubBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func decodeKeyLen(s string, want int) bool {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return err == nil && len(b) == want
}

func readPushBody(w http.ResponseWriter, r *http.Request) (pushSubBody, bool) {
	var b pushSubBody
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return b, false
	}
	return b, true
}

func (s *Server) handlePushVAPIDKey(w http.ResponseWriter, r *http.Request) {
	if s.pushKey == nil || s.pushKey() == "" {
		http.Error(w, "web push is not enabled", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.pushKey()})
}

func (s *Server) handlePushStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountPushSubs()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.pushKey != nil && s.pushKey() != "",
		"devices": n,
	})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushKey == nil || s.pushKey() == "" {
		http.Error(w, "web push is not enabled", http.StatusServiceUnavailable)
		return
	}
	b, ok := readPushBody(w, r)
	if !ok {
		return
	}
	if err := validatePushEndpoint(b.Endpoint); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// p256dh is an uncompressed P-256 point (65 bytes), auth a 16-byte secret.
	if !decodeKeyLen(b.Keys.P256dh, 65) || !decodeKeyLen(b.Keys.Auth, 16) {
		http.Error(w, "bad subscription keys", http.StatusBadRequest)
		return
	}
	subs, err := s.store.ListPushSubs()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	known := false
	for _, p := range subs {
		if p.Endpoint == b.Endpoint {
			known = true
		}
	}
	if !known && len(subs) >= maxPushDevices {
		http.Error(w, "too many subscribed devices", http.StatusConflict)
		return
	}
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if err := s.store.UpsertPushSub(store.PushSub{
		Endpoint: b.Endpoint, P256dh: b.Keys.P256dh, Auth: b.Keys.Auth, UserAgent: ua,
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	devices := len(subs)
	if !known {
		devices++
	}
	s.logger.Info("push subscription saved", "devices", devices)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "subscribed"})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	b, ok := readPushBody(w, r)
	if !ok {
		return
	}
	if b.Endpoint == "" {
		http.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	if err := s.store.DeletePushSub(b.Endpoint); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.pushTest == nil {
		http.Error(w, "web push is not enabled", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, err := s.pushTest(ctx)
	if err != nil {
		// Not a 5xx: behind Cloudflare a 502/504 from the origin is replaced
		// by Cloudflare's own HTML error page, which hid the real reason.
		// The request itself worked; report the push failure in the body.
		writeJSON(w, http.StatusOK, map[string]any{"sent": 0, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": n})
}
