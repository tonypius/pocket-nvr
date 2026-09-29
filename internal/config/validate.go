package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var camIDRe = regexp.MustCompile(`^[a-z0-9_]+$`)

func (c *Config) applyDefaults() {
	d := &c.System
	if d.BasePath == "" {
		d.BasePath = "/data/nvr"
	}
	if d.LogLevel == "" {
		d.LogLevel = "info"
	}
	if d.ChargeCapPct == 0 {
		d.ChargeCapPct = 65
	}
	if d.StorageMinFreeGB == 0 {
		d.StorageMinFreeGB = 8
	}

	m := &c.Media
	if m.RTSPAddress == "" {
		m.RTSPAddress = ":8554"
	}
	if m.WebRTCAddress == "" {
		m.WebRTCAddress = ":8889"
	}
	if m.HLSAddress == "" {
		m.HLSAddress = ":8888"
	}
	if m.PlaybackAddress == "" {
		m.PlaybackAddress = ":9997"
	}
	if m.AuthUser == "" {
		m.AuthUser = "viewer"
	}

	for i := range c.Cameras {
		cam := &c.Cameras[i]
		if cam.MainURL == "" && cam.ID != "" {
			// Derive restream URLs from the MediaMTX address (FR-CFG-4).
			cam.MainURL = "rtsp://127.0.0.1" + c.Media.RTSPAddress + "/" + cam.ID
			cam.SubURL = "rtsp://127.0.0.1" + c.Media.RTSPAddress + "/" + cam.ID + "_sub"
		}
		dt := &cam.Detect
		if dt.FPS == 0 {
			dt.FPS = 5
		}
		if dt.Threshold == 0 {
			dt.Threshold = 0.5
		}
		if dt.EnterFrames == 0 {
			dt.EnterFrames = 3
		}
		if dt.ExitFrames == 0 {
			dt.ExitFrames = 8
		}
		if dt.MotionMinArea == 0 {
			dt.MotionMinArea = 0.02
		}
		if dt.Anchor == "" {
			dt.Anchor = "bottom_center"
		}
		if len(dt.Classes) == 0 {
			dt.Classes = []string{"person"}
		}
	}

	r := &c.Recording
	if r.Mode == "" {
		r.Mode = "continuous"
	}
	if r.SegmentSeconds == 0 {
		r.SegmentSeconds = 300
	}
	if r.RetentionDays == 0 {
		r.RetentionDays = 7
	}
	if r.SizeCapGB == 0 {
		r.SizeCapGB = 128
	}

	e := &c.Events
	if e.ClipPreSeconds == 0 {
		e.ClipPreSeconds = 5
	}
	if e.ClipPostSeconds == 0 {
		e.ClipPostSeconds = 10
	}
	if e.RetentionDays == 0 {
		e.RetentionDays = 30
	}
	if e.RetentionMaxCount == 0 {
		e.RetentionMaxCount = 5000
	}

	de := &c.Detection
	if de.Model == "" {
		de.Model = "yolo11n"
	}
	if de.InputSize == 0 {
		de.InputSize = 640
	}
	if de.Backend == "" {
		de.Backend = "vulkan"
	}
	if de.QueueMax == 0 {
		de.QueueMax = 32
	}
	if de.TempHighC == 0 {
		de.TempHighC = 75
	}
	if de.TempLowC == 0 {
		de.TempLowC = 65
	}

	n := &c.Notifications
	if n.Provider == "" {
		n.Provider = "ntfy"
	}
	if n.CooldownSeconds == 0 {
		n.CooldownSeconds = 60
	}
	if n.GlobalMinGapSeconds == 0 {
		n.GlobalMinGapSeconds = 30
	}

	a := &c.API
	if a.Bind == "" {
		a.Bind = "0.0.0.0:8099"
	}
	if a.Auth == "" {
		a.Auth = "token"
	}
}

// Validate aggregates every problem into one error list (FR-CFG-3).
func (c *Config) Validate() []string {
	var errs []string
	errf := func(format string, a ...any) {
		errs = append(errs, fmt.Sprintf(format, a...))
	}

	switch c.System.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errf("system.log_level %q: want debug|info|warn|error", c.System.LogLevel)
	}
	if c.System.BasePath == "" {
		errf("system.base_path: required")
	}
	if c.System.ChargeCapPct < 50 || c.System.ChargeCapPct > 90 {
		errf("system.charge_cap_pct %d: want 50..90 (battery longevity)", c.System.ChargeCapPct)
	}

	if _, _, err := net.SplitHostPort(c.Media.RTSPAddress); err != nil {
		errf("media.rtsp_address %q: %v", c.Media.RTSPAddress, err)
	}
	if _, _, err := net.SplitHostPort(c.Media.WebRTCAddress); err != nil {
		errf("media.webrtc_address %q: %v", c.Media.WebRTCAddress, err)
	}
	if _, _, err := net.SplitHostPort(c.Media.PlaybackAddress); err != nil {
		err0 := fmt.Sprintf("media.playback_address %q: %v", c.Media.PlaybackAddress, err)
		errs = append(errs, err0)
	}
	if _, _, err := net.SplitHostPort(c.Media.HLSAddress); err != nil {
		errf("media.hls_address %q: %v", c.Media.HLSAddress, err)
	}

	if len(c.Cameras) == 0 {
		errf("cameras: at least one required")
	}
	seen := map[string]bool{}
	for i, cam := range c.Cameras {
		id := cam.ID
		if !camIDRe.MatchString(id) {
			errf("cameras[%d].id %q: want lowercase slug [a-z0-9_]", i, id)
		}
		if seen[id] {
			errf("cameras[%d].id %q: duplicate", i, id)
		}
		seen[id] = true
		for _, u := range []struct{ name, val string }{
			{"source_main", cam.SourceMain}, {"source_sub", cam.SourceSub},
			{"main_url", cam.MainURL}, {"sub_url", cam.SubURL},
		} {
			p, err := url.Parse(u.val)
			if err != nil || p.Scheme != "rtsp" || p.Host == "" {
				errf("cameras[%s].%s %q: want rtsp://host[:port]/path", id, u.name, redactURL(u.val))
			}
		}
		d := cam.Detect
		if d.FPS < 1 || d.FPS > 30 {
			errf("cameras[%s].detect.fps %d: want 1..30", id, d.FPS)
		}
		if d.Threshold < 0.01 || d.Threshold > 1 {
			errf("cameras[%s].detect.threshold %v: want 0.01..1", id, d.Threshold)
		}
		if d.EnterFrames < 1 {
			errf("cameras[%s].detect.enter_frames %d: want >=1", id, d.EnterFrames)
		}
		if d.ExitFrames < 1 {
			errf("cameras[%s].detect.exit_frames %d: want >=1", id, d.ExitFrames)
		}
		if d.MotionMinArea <= 0 || d.MotionMinArea >= 1 {
			errf("cameras[%s].detect.motion_min_area %v: want 0..1 fraction", id, d.MotionMinArea)
		}
		if d.Anchor != "bottom_center" && d.Anchor != "centroid" {
			errf("cameras[%s].detect.anchor %q: want bottom_center|centroid", id, d.Anchor)
		}
		for zi, z := range d.Zones {
			if z.Name == "" {
				errf("cameras[%s].detect.zones[%d].name: required", id, zi)
			}
			if len(z.Polygon) < 3 {
				errf("cameras[%s].detect.zones[%d] %q: polygon needs >=3 points", id, zi, z.Name)
			}
		}
	}

	if c.Recording.Mode != "continuous" && c.Recording.Mode != "event_only" {
		errf("recording.mode %q: want continuous|event_only", c.Recording.Mode)
	}
	if c.Recording.SegmentSeconds < 10 || c.Recording.SegmentSeconds > 3600 {
		errf("recording.segment_seconds %d: want 10..3600", c.Recording.SegmentSeconds)
	}
	if c.Recording.RetentionDays < 1 {
		errf("recording.retention_days %d: want >=1", c.Recording.RetentionDays)
	}
	if c.Events.ClipPreSeconds < 0 || c.Events.ClipPostSeconds < 0 {
		errf("events.clip_*_seconds: want >=0")
	}
	if c.Recording.SegmentSeconds > c.Events.ClipPreSeconds+c.Events.ClipPostSeconds &&
		c.Events.ClipPreSeconds+c.Events.ClipPostSeconds > 0 {
		// Not fatal: pre-roll comes from continuous segments, which may span
		// two files. Flag only if padding is absurdly small vs segment.
		_ = 0
	}

	for i, cam := range c.Cameras {
		for _, cls := range cam.Detect.Classes {
			if cls == "" || cls != strings.ToLower(cls) || strings.ContainsAny(cls, " \t") {
				errf("cameras[%d].detect.classes %q: lowercase COCO class names only", i, cls)
			}
		}
	}

	if c.Detection.InputSize < 320 || c.Detection.InputSize > 1280 || c.Detection.InputSize%32 != 0 {
		errf("detection.input_size %d: want multiple of 32 in 320..1280", c.Detection.InputSize)
	}
	if c.Detection.Backend != "vulkan" && c.Detection.Backend != "cpu" {
		errf("detection.backend %q: want vulkan|cpu", c.Detection.Backend)
	}
	if c.Detection.QueueMax < 1 {
		errf("detection.queue_max %d: want >=1", c.Detection.QueueMax)
	}
	if c.Detection.TempHighC < 45 || c.Detection.TempHighC > 95 ||
		c.Detection.TempLowC >= c.Detection.TempHighC {
		errf("detection.temp_high_c/low_c %d/%d: want 45..95 with low < high (hysteresis)",
			c.Detection.TempHighC, c.Detection.TempLowC)
	}

	switch c.Notifications.Provider {
	case "ntfy", "telegram", "both":
	default:
		errf("notifications.provider %q: want ntfy|telegram|both", c.Notifications.Provider)
	}
	if c.Notifications.CooldownSeconds < 0 {
		errf("notifications.cooldown_seconds %d: want >=0", c.Notifications.CooldownSeconds)
	}
	if c.Notifications.GlobalMinGapSeconds < 0 {
		errf("notifications.global_min_gap_seconds %d: want >=0", c.Notifications.GlobalMinGapSeconds)
	}
	if c.Notifications.BaseURL != "" &&
		!strings.HasPrefix(c.Notifications.BaseURL, "http://") &&
		!strings.HasPrefix(c.Notifications.BaseURL, "https://") {
		errf("notifications.base_url %q: want http(s):// origin", c.Notifications.BaseURL)
	}
	if c.Notifications.QuietStart != "" {
		if _, err := time.Parse("15:04", c.Notifications.QuietStart); err != nil {
			errf("notifications.quiet_hours.start %q: want HH:MM", c.Notifications.QuietStart)
		}
	}
	if c.Notifications.QuietEnd != "" {
		if _, err := time.Parse("15:04", c.Notifications.QuietEnd); err != nil {
			errf("notifications.quiet_hours.end %q: want HH:MM", c.Notifications.QuietEnd)
		}
	}

	if _, _, err := net.SplitHostPort(c.API.Bind); err != nil {
		errf("api.bind %q: want host:port", c.API.Bind)
	}
	if c.API.Auth != "token" {
		errf("api.auth %q: only token supported in v1", c.API.Auth)
	}
	return errs
}

func redactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	if p.User != nil {
		if _, pass := p.User.Password(); pass {
			p.User = url.UserPassword(p.User.Username(), "REDACTED")
		}
	}
	return p.String()
}

// HumanTime renders seconds as a duration string for the mediamtx generator.
func HumanTime(seconds int) string {
	return (time.Duration(seconds) * time.Second).String()
}

// SizeCapBytes returns recording.size_cap_gb in bytes.
func (c *Config) SizeCapBytes() int64 {
	return int64(c.Recording.SizeCapGB) * 1024 * 1024 * 1024
}

// QuietMinutes converts "HH:MM" to minutes-since-midnight, or -1 if unset.
func QuietMinutes(hhmm string) int {
	if hhmm == "" {
		return -1
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return -1
	}
	return t.Hour()*60 + t.Minute()
}

var _ = strconv.Itoa // keep imports stable if helpers change
