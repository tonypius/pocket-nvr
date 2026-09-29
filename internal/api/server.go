// Package api is COMP-6: the local REST API + static UI host (FR-API-*).
// All routes require token auth (FR-API-5). Static files under ui/ are
// served at / and rely on the same ?token= deep links notifications use.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"pocketnvr/internal/config"
	"pocketnvr/internal/discover"
	"pocketnvr/internal/mediamtx"
	"pocketnvr/internal/store"
)

const Version = "0.1.0"

// Server carries shared state; cfg is swapped atomically on SIGHUP reload.
type Server struct {
	cfg      atomic.Pointer[config.Config]
	redact   atomic.Pointer[config.Redactor]
	store    *store.Store
	token    atomic.Pointer[string]
	start    time.Time
	logger   *slog.Logger
	uiDir    string
	mtxReady func() bool // is MediaMTX known healthy? stub until wired
	// detector heartbeats (FR-DET-12) + event handler hook (COMP-5 path).
	gauges          atomic.Pointer[DetectorGauges]
	gaugesAt        atomic.Int64
	storageDegraded atomic.Bool
	// config/secrets administration (FR-API-4, FR-CFG-2).
	cfgDir  string
	hupSelf func() // triggers the SIGHUP reload path in main
	// eventHandler fires after each stored detector event. Guarded by
	// swapMu for SIGHUP reloads.
	swapMu       sync.RWMutex
	eventHandler func(cameraID, cameraName string, eventID, startTS, endTS int64, score float64, snapshot []byte)
	// SSE live event push (B3)
	sseMu      sync.Mutex
	sseClients []chan []byte
}

// SetMtxProbe registers the MediaMTX liveness probe used by /api/health.
func (s *Server) SetMtxProbe(fn func() bool) { s.mtxReady = fn }

// SetDirs sets the directory holding config.yaml + secrets.yaml so the
// admin API can write validated config and merge secrets.
func (s *Server) SetDirs(dir string) { s.cfgDir = dir }

// SetHupSelf registers the "reload now" hook (SIGHUP to own process).
func (s *Server) SetHupSelf(fn func()) { s.hupSelf = fn }

// SetStorageDegraded flags the FR-SUP-8 low-space state.
func (s *Server) SetStorageDegraded(v bool) { s.storageDegraded.Store(v) }

// FreeDiskBytes exposes the platform disk probe to other components.
func FreeDiskBytes(path string) (uint64, bool) { return freeDiskBytes(path) }

func New(cfg *config.Config, redactor *config.Redactor, token string, st *store.Store, logger *slog.Logger, uiDir string) *Server {
	s := &Server{store: st, start: time.Now(), logger: logger, uiDir: uiDir}
	s.cfg.Store(cfg)
	s.redact.Store(redactor)
	s.token.Store(&token)
	return s
}

// SwapConfig installs a reloaded config (FR-API-4 reload path).
func (s *Server) SwapConfig(cfg *config.Config, redactor *config.Redactor) {
	s.cfg.Store(cfg)
	s.redact.Store(redactor)
}

// SetEventHandler registers the post-store callback for detector events.
func (s *Server) SetEventHandler(fn func(cameraID, cameraName string, eventID, startTS, endTS int64, score float64, snapshot []byte)) {
	s.swapMu.Lock()
	defer s.swapMu.Unlock()
	s.eventHandler = fn
}

// Handler builds the routed, auth-wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("PUT /api/config", s.handleConfigPut)
	mux.HandleFunc("POST /api/secrets", s.handleSecretsPost)
	mux.HandleFunc("POST /api/discover", s.handleDiscover)
	mux.HandleFunc("GET /api/events/stream", s.handleEventsStream)
	mux.HandleFunc("GET /api/cameras", s.handleCameras)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/events/{id}", s.handleEvent)
	mux.HandleFunc("GET /api/events/{id}/snapshot", s.eventAsset("snapshot_path"))
	mux.HandleFunc("GET /api/events/{id}/crop", s.eventAsset("crop_path"))
	mux.HandleFunc("GET /api/events/{id}/clip", s.eventAsset("clip_path"))
	mux.HandleFunc("GET /api/live/{camera}", s.handleLive)
	mux.HandleFunc("GET /api/live/hls/{camera}/", s.handleHLSProxy)
	mux.HandleFunc("POST /api/live/whep/{camera}", s.handleWHEPProxy)
	mux.HandleFunc("GET /api/playback/{camera}/list", s.handlePlaybackList)
	mux.HandleFunc("GET /api/playback/{camera}/get", s.handlePlaybackGet)
	// Internal ingest for the detector (COMP-3 → COMP-4, FR-DET-9/12).
	mux.HandleFunc("POST /api/internal/events", s.handleEventPost)
	mux.HandleFunc("POST /api/internal/metrics", s.handleDetectorMetrics)
	// Static UI last (catch-all under /).
	mux.Handle("GET /", s.staticUI())

	return s.auth(mux)
}

// auth enforces the token on all /api/ requests: Authorization: Bearer,
// an X-Api-Token header, or ?token= (needed for <img>/<video> tags and
// push deep links). 401 otherwise (FR-API-5). The static app shell is
// served unauthenticated — it contains no data and the login gate needs
// to render before a token exists; every API call still requires it.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		tok := *s.token.Load()
		if tok != "" {
			got := r.Header.Get("Authorization")
			got = strings.TrimPrefix(got, "Bearer ")
			if got == "" {
				got = r.Header.Get("X-Api-Token")
			}
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if got != tok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="pocketnvr"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	status := "ok"
	if s.storageDegraded.Load() {
		status = "degraded"
	}
	health := map[string]any{
		"status":   status,
		"version":  Version,
		"uptime_s": int64(time.Since(s.start).Seconds()),
		"components": map[string]string{
			"nvrd":     "up",
			"mediamtx": s.mtxStatus(),
			"detector": s.detectorStatus(),
		},
		"storage_degraded": s.storageDegraded.Load(),
		"storage":          storageInfo(cfg.System.BasePath),
		"temp_c":           socTempC(),
	}
	writeJSON(w, http.StatusOK, health)
}

func (s *Server) mtxStatus() string {
	if s.mtxReady != nil && s.mtxReady() {
		return "up"
	}
	return "unknown"
}

func storageInfo(base string) map[string]any {
	out := map[string]any{"base_path": base, "free_bytes": nil}
	if free, ok := freeDiskBytes(base); ok {
		out["free_bytes"] = free
	}
	return out
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	count, _ := s.store.CountEvents()
	out := map[string]any{
		"uptime_s":     int64(time.Since(s.start).Seconds()),
		"events_total": count,
		"note":         "nvrd-level counters; detector gauges under \"detector\"",
	}
	if g := s.gaugesForMetrics(); g != nil {
		out["detector"] = g
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	// Field-path templates from load time render the redacted view
	// (FR-CFG-2): every field that referenced secrets comes back in its
	// ${secret:key} template form — no value-based matching at all.
	cfg := s.cfg.Load().DeepCopy()
	redacted := s.redact.Load().RedactedCopy(cfg)
	writeJSON(w, http.StatusOK, redacted)
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	// FR-API-4: validate → atomic write → SIGHUP self. The body uses the
	// same JSON shape as GET /api/config; ${secret:key} placeholders
	// survive the round-trip (redaction) and re-resolve on reload, so
	// secrets never transit this endpoint.
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "bad config body: "+err.Error(), 400)
		return
	}
	// validate a copy with placeholders dummied out; write the original
	// (placeholders intact) so secrets keep resolving on reload
	if err := cfg.WithPlaceholdersResolved().Finalize(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	y, err := yaml.Marshal(&cfg)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.cfgDir == "" {
		http.Error(w, "config dir not configured", 500)
		return
	}
	dst := filepath.Join(s.cfgDir, "config.yaml")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, y, 0o600); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.hupSelf != nil {
		s.hupSelf()
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted, reloading"})
}

// handleSecretsPost merges key/values into secrets.yaml (FR-CFG-2).
// Values are write-only: no GET ever returns them.
func (s *Server) handleSecretsPost(w http.ResponseWriter, r *http.Request) {
	if s.cfgDir == "" {
		http.Error(w, "config dir not configured", 500)
		return
	}
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in) == 0 {
		http.Error(w, "bad secrets body", 400)
		return
	}
	for k, v := range in {
		if k == "" || v == "" || strings.ContainsAny(k, " \n\t:#") {
			http.Error(w, "bad key or value", 400)
			return
		}
	}
	path := filepath.Join(s.cfgDir, "secrets.yaml")
	existing, err := config.LoadSecrets(path)
	if err != nil {
		existing = map[string]string{}
	}
	for k, v := range in {
		existing[k] = v
	}
	out, err := yaml.Marshal(existing)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.hupSelf != nil {
		s.hupSelf()
	}
	s.logger.Info("secrets updated", "keys", len(in))
	writeJSON(w, http.StatusOK, map[string]any{"updated": len(in)})
}

// handleDiscover scans the local /24 for ONVIF (WS-Discovery) and RTSP
// cameras. Runs synchronously with a hard budget (~8 s).
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"results": discover.Scan(ctx),
		"scanned": time.Now().Unix(),
	})
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	type cfgDetect struct {
		FPS       int      `json:"fps"`
		Threshold float64  `json:"threshold"`
		Zones     []string `json:"zones"`
	}
	type live struct {
		WebRTC   string `json:"webrtc"`    // nvrd-proxied WHEP signaling (media is peer-to-peer UDP)
		Page     string `json:"page"`      // MediaMTX WebRTC web page
		HLS      string `json:"hls"`       // HLS playlist (direct)
		HLSProxy string `json:"hls_proxy"` // nvrd-proxied HLS (works everywhere)
	}
	type out struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Streams struct {
			Main string `json:"main"`
			Sub  string `json:"sub"`
		} `json:"streams"`
		Live   live      `json:"live"`
		Detect cfgDetect `json:"detect"`
	}
	// live endpoints are addressed via the requesting host (LAN/Tailscale)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := hostnameOnly(r.Host)
	hlsBase := scheme + "://" + net.JoinHostPort(host, portOf(cfg.Media.HLSAddress))
	res := make([]out, 0, len(cfg.Cameras))
	for _, c := range cfg.Cameras {
		main, sub := mediamtx.LocalStreamURLs(cfg, c.ID)
		zones := make([]string, 0, len(c.Detect.Zones))
		for _, z := range c.Detect.Zones {
			zones = append(zones, z.Name)
		}
		var o out
		o.ID, o.Name, o.Enabled = c.ID, c.Name, c.IsEnabled()
		o.Streams.Main, o.Streams.Sub = main, sub
		o.Live.WebRTC = "/api/live/whep/" + c.ID
		o.Live.Page = hlsBase + "/" + c.ID // MediaMTX web page (HLS host)
		o.Live.HLS = hlsBase + "/" + c.ID + "/index.m3u8"
		o.Live.HLSProxy = "/api/live/hls/" + c.ID + "/index.m3u8"
		o.Detect = cfgDetect{FPS: c.Detect.FPS, Threshold: c.Detect.Threshold, Zones: zones}
		res = append(res, o)
	}
	writeJSON(w, http.StatusOK, res)
}

// hostnameOnly strips any port from a Host header.
func hostnameOnly(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// portOf extracts the port from ":8889" or "0.0.0.0:8889".
func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return strings.TrimPrefix(addr, ":")
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	camera := q.Get("camera")
	label := q.Get("label")
	from := atoi64(q.Get("from"))
	to := atoi64(q.Get("to"))
	limit := atoi64(q.Get("limit"))
	events, err := s.store.ListEvents(camera, from, to, label, int(limit))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	e, err := s.store.Event(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if e == nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) eventAsset(field string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := atoi64(r.PathValue("id"))
		e, err := s.store.Event(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if e == nil {
			http.Error(w, "not found", 404)
			return
		}
		var p *string
		switch field {
		case "snapshot_path":
			p = e.SnapshotPath
		case "crop_path":
			p = e.CropPath
		default:
			p = e.ClipPath
		}
		if p == nil || *p == "" {
			http.Error(w, "asset not available", 404)
			return
		}
		f, err := os.Open(*p)
		if err != nil {
			http.Error(w, "asset missing on disk", 404)
			return
		}
		defer f.Close()
		if field == "clip_path" {
			w.Header().Set("Content-Type", "video/mp4")
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
		}
		// ServeContent gives Range support for video seeks.
		http.ServeContent(w, r, filepath.Base(*p), time.UnixMilli(e.StartTS), f)
	}
}

// handleHLSProxy reverse-proxies MediaMTX's HLS playlist and segments
// through nvrd (FR-API-2). Browsers authenticate to nvrd; nvrd fetches
// from MediaMTX over localhost (passwordless by design), so live video
// works from any network path — including TCP-only tunnels where
// WebRTC's UDP ICE cannot traverse.
func (s *Server) handleHLSProxy(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	cam := r.PathValue("camera")
	known := false
	for _, c := range cfg.Cameras {
		if c.ID == cam {
			known = true
			break
		}
	}
	if !known {
		http.Error(w, "unknown camera", 404)
		return
	}
	_, hlsPort := portOf(cfg.Media.HLSAddress), strings.TrimPrefix(cfg.Media.HLSAddress, ":")
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", hlsPort)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// MediaMTX redirects the entry playlist to a timestamped path with an
	// absolute-path Location — re-prefix it so the client stays on the proxy.
	proxy.ModifyResponse = func(resp *http.Response) error {
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") {
			resp.Header.Set("Location", "/api/live/hls"+loc)
		}
		return nil
	}
	// strip /api/live/hls prefix: /api/live/hls/<cam>/<rest> → /<cam>/<rest>
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/live/hls")
	r.Host = target.Host
	proxy.ServeHTTP(w, r)
}

// handleWHEPProxy proxies the WHEP signaling POST to MediaMTX
// (localhost = passwordless by design). The browser authenticates to nvrd;
// media then flows browser↔phone over UDP directly (LAN/Tailscale).
func (s *Server) handleWHEPProxy(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	cam := r.PathValue("camera")
	known := false
	for _, c := range cfg.Cameras {
		if c.ID == cam {
			known = true
			break
		}
	}
	if !known {
		http.Error(w, "unknown camera", 404)
		return
	}
	_, webRTCPort := portOf(cfg.Media.WebRTCAddress), strings.TrimPrefix(cfg.Media.WebRTCAddress, ":")
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", webRTCPort)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.URL.Path = "/" + cam + "/whep"
		req.Host = target.Host
	}
	proxy.ServeHTTP(w, r)
}

// handleLive redirects to MediaMTX WebRTC page for the camera (FR-API-2).
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	cam := r.PathValue("camera")
	for _, c := range cfg.Cameras {
		if c.ID == cam && c.IsEnabled() {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				_, webrtcPort, _ := net.SplitHostPort(cfg.Media.WebRTCAddress)
				host = net.JoinHostPort(h, webrtcPort)
			}
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			http.Redirect(w, r, scheme+"://"+host+"/"+cam, http.StatusFound)
			return
		}
	}
	http.Error(w, "unknown camera: "+cam, 404)
}

// handleEventPost is the internal ingest for the detector (FR-DET-9).
// Auth: same token, but expected to be called from 127.0.0.1 only.
func (s *Server) handleEventPost(w http.ResponseWriter, r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || host != "127.0.0.1" {
		http.Error(w, "internal endpoint", http.StatusForbidden)
		return
	}
	bodyBytes, _ := io.ReadAll(r.Body)
	var in struct {
		CameraID    string  `json:"camera_id"`
		Label       string  `json:"label"`
		Score       float64 `json:"score"`
		StartTS     int64   `json:"start_ts"`
		EndTS       *int64  `json:"end_ts"`
		Zone        *string `json:"zone"`
		BBox        []int   `json:"bbox"`
		SnapshotB64 string  `json:"snapshot_jpeg_b64"`
		CropB64     string  `json:"crop_jpeg_b64"`
	}
	if err := json.Unmarshal(bodyBytes, &in); err != nil || in.CameraID == "" {
		s.logger.Error("bad event body", "err", err,
			"body_len", len(bodyBytes),
			"body_start", string(bodyBytes[:min(300, len(bodyBytes))]))
		http.Error(w, "bad event body", 400)
		return
	}
	var bbox *string
	if len(in.BBox) == 4 {
		b := fmt.Sprintf("[%d,%d,%d,%d]", in.BBox[0], in.BBox[1], in.BBox[2], in.BBox[3])
		bbox = &b
	}
	id, err := s.store.StartEvent(store.ActiveEvent{
		CameraID: in.CameraID, Label: in.Label, Score: in.Score,
		StartTS: in.StartTS, Zone: in.Zone, CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if in.EndTS != nil {
		_ = s.store.EndEvent(id, *in.EndTS)
	}
	_ = s.store.UpdatePeak(id, in.Score, bbox)

	// Snapshot (base64 JPEG, FRS §5.2) is persisted with the event once
	// COMP-3 sends it; pass the bytes to the notify hook either way.
	var snapBytes, cropBytes []byte
	if in.SnapshotB64 != "" {
		if b, err := base64.StdEncoding.DecodeString(in.SnapshotB64); err == nil {
			snapBytes = b
		} else {
			s.logger.Warn("bad snapshot b64", "event", id, "err", err)
		}
	}
	if in.CropB64 != "" {
		if b, err := base64.StdEncoding.DecodeString(in.CropB64); err == nil {
			cropBytes = b
		} else {
			s.logger.Warn("bad crop b64", "event", id, "err", err)
		}
	}
	// Persist the snapshot (FR-EVT-2) and the cropped object thumb (B1).
	if len(snapBytes) > 0 {
		p := filepath.Join(s.cfg.Load().System.BasePath, "snapshots", fmt.Sprintf("%d.jpg", id))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			if err := os.WriteFile(p, snapBytes, 0o644); err == nil {
				_ = s.store.SetAssets(id, &p, nil)
			} else {
				s.logger.Warn("snapshot write", "event", id, "err", err)
			}
		}
	}
	if len(cropBytes) > 0 {
		p := filepath.Join(s.cfg.Load().System.BasePath, "snapshots", fmt.Sprintf("%d-crop.jpg", id))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			if err := os.WriteFile(p, cropBytes, 0o644); err == nil {
				_ = s.store.SetCrop(id, p)
			}
		}
	}

	s.swapMu.RLock()
	fn := s.eventHandler
	s.swapMu.RUnlock()
	if fn != nil {
		camName := in.CameraID
		for _, c := range s.cfg.Load().Cameras {
			if c.ID == in.CameraID {
				camName = c.Name
				break
			}
		}
		endTS := int64(0)
		if in.EndTS != nil {
			endTS = *in.EndTS
		}
		fn(in.CameraID, camName, id, in.StartTS, endTS, in.Score, snapBytes)
	}

	// B3: broadcast to SSE clients (UI event rail + live counters)
	if evJSON, err := json.Marshal(map[string]any{
		"id": id, "camera_id": in.CameraID, "label": in.Label,
		"score": in.Score, "start_ts": in.StartTS, "end_ts": in.EndTS,
	}); err == nil {
		s.publishEvent(evJSON)
	}

	s.logger.Info("event ingested", "camera", in.CameraID, "id", id, "score", in.Score)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) staticUI() http.Handler {
	fs := http.FileServer(http.Dir(s.uiDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(filepath.Join(s.uiDir, "index.html")); err != nil {
			http.Error(w, "UI not built (ui/ missing index.html)", 404)
			return
		}
		// SPA fallback: unknown paths get the app shell.
		p := filepath.Join(s.uiDir, filepath.Clean("/"+r.URL.Path))
		if _, err := os.Stat(p); err != nil {
			r.URL.Path = "/"
		}
		if strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		// shell files must never be served from a stale HTTP cache — the
		// login/tile logic lives here and stale copies break the UI
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") ||
			strings.HasSuffix(r.URL.Path, ".js") ||
			strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Cache-Control", "no-store")
		}
		fs.ServeHTTP(w, r)
	})
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// socTempC reads the first thermal zone (phone: SoC). Best effort; absent
// on dev machines.
func socTempC() any {
	if runtime.GOOS != "linux" {
		return nil
	}
	b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return nil
	}
	milli := atoi64(strings.TrimSpace(string(b)))
	if milli == 0 {
		return nil
	}
	return float64(milli) / 1000.0
}

func (s *Server) addSSEClient(ch chan []byte) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	s.sseClients = append(s.sseClients, ch)
}

func (s *Server) removeSSEClient(ch chan []byte) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for i, c := range s.sseClients {
		if c == ch {
			s.sseClients = append(s.sseClients[:i], s.sseClients[i+1:]...)
			return
		}
	}
}
