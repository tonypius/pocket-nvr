// Package config implements COMP-8: the single config.yaml source of truth
// (FR-CFG-1) with secrets interpolation from secrets.yaml (FR-CFG-2),
// validation (FR-CFG-3) and defaults (FR-CFG-4). Schema mirrors FRS §6.3,
// plus a `media:` section for MediaMTX endpoint addresses.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretPrefix marks a secret reference inside a config string value:
// e.g. "rtsp://${secret:front_user}:${secret:front_pass}@192.168.0.20/stream1".
const SecretPrefix = "${secret:"

type Config struct {
	System        System        `yaml:"system" json:"system"`
	Media         Media         `yaml:"media" json:"media"`
	Cameras       []Camera      `yaml:"cameras" json:"cameras"`
	Recording     Recording     `yaml:"recording" json:"recording"`
	Events        Events        `yaml:"events" json:"events"`
	Detection     Detection     `yaml:"detection" json:"detection"`
	Notifications Notifications `yaml:"notifications" json:"notifications"`
	API           API           `yaml:"api" json:"api"`
	Remote        Remote        `yaml:"remote" json:"remote"`
}

type System struct {
	BasePath         string `yaml:"base_path" json:"base_path"`
	LogLevel         string `yaml:"log_level" json:"log_level"`
	Wakelock         bool   `yaml:"wakelock" json:"wakelock"`
	ChargeCapPct     int    `yaml:"charge_cap_pct" json:"charge_cap_pct"`
	StorageMinFreeGB int    `yaml:"storage_min_free_gb" json:"storage_min_free_gb"`
}

// Media holds the MediaMTX endpoint addresses the generator and the live-view
// redirect need. Extension to the FRS schema; all fields have defaults.
type Media struct {
	RTSPAddress     string `yaml:"rtsp_address" json:"rtsp_address"`         // restream server
	WebRTCAddress   string `yaml:"webrtc_address" json:"webrtc_address"`     // browser live view
	HLSAddress      string `yaml:"hls_address" json:"hls_address"`           // browser live view fallback
	PlaybackAddress string `yaml:"playback_address" json:"playback_address"` // recordings playback server (B4)
	AuthUser        string `yaml:"auth_user" json:"auth_user"`               // mediamtx viewer account name
}

type Detect struct {
	FPS           int      `yaml:"fps" json:"fps"`
	Threshold     float64  `yaml:"threshold" json:"threshold"`
	EnterFrames   int      `yaml:"enter_frames" json:"enter_frames"`
	ExitFrames    int      `yaml:"exit_frames" json:"exit_frames"`
	MotionMinArea float64  `yaml:"motion_min_area" json:"motion_min_area"`
	Anchor        string   `yaml:"anchor" json:"anchor"`
	Zones         []Zone   `yaml:"zones" json:"zones"`
	Classes       []string `yaml:"classes" json:"classes"` // COCO classes to detect (B2)
}

type Zone struct {
	Name    string       `yaml:"name" json:"name"`
	Polygon [][2]float64 `yaml:"polygon" json:"polygon"`
}

type Camera struct {
	ID         string `yaml:"id" json:"id"`
	Name       string `yaml:"name" json:"name"`
	MainURL    string `yaml:"main_url" json:"main_url"`       // local restream (MediaMTX); derived when empty
	SubURL     string `yaml:"sub_url" json:"sub_url"`         // local restream (MediaMTX); derived when empty
	SourceMain string `yaml:"source_main" json:"source_main"` // real camera
	SourceSub  string `yaml:"source_sub" json:"source_sub"`   // real camera
	Enabled    *bool  `yaml:"enabled" json:"enabled"`         // nil = enabled (FR-CFG-4 minimal config)
	Detect     Detect `yaml:"detect" json:"detect"`
}

// IsEnabled reports whether the camera participates in ingest/detection.
func (c *Camera) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type Recording struct {
	Mode           string `yaml:"mode" json:"mode"` // continuous | event_only
	SegmentSeconds int    `yaml:"segment_seconds" json:"segment_seconds"`
	RetentionDays  int    `yaml:"retention_days" json:"retention_days"`
	SizeCapGB      int    `yaml:"size_cap_gb" json:"size_cap_gb"`
}

type Events struct {
	ClipPreSeconds    int `yaml:"clip_pre_seconds" json:"clip_pre_seconds"`
	ClipPostSeconds   int `yaml:"clip_post_seconds" json:"clip_post_seconds"`
	RetentionDays     int `yaml:"retention_days" json:"retention_days"`
	RetentionMaxCount int `yaml:"retention_max_count" json:"retention_max_count"`
}

type Detection struct {
	Model     string `yaml:"model" json:"model"`
	InputSize int    `yaml:"input_size" json:"input_size"`
	Backend   string `yaml:"backend" json:"backend"` // vulkan | cpu
	QueueMax  int    `yaml:"queue_max" json:"queue_max"`
	TempHighC int    `yaml:"temp_high_c" json:"temp_high_c"` // governor: step down at (FR-DET-10)
	TempLowC  int    `yaml:"temp_low_c" json:"temp_low_c"`   // governor: resume at
}

type Notifications struct {
	Provider            string `yaml:"provider" json:"provider"` // ntfy | telegram | both
	CooldownSeconds     int    `yaml:"cooldown_seconds" json:"cooldown_seconds"`
	GlobalMinGapSeconds int    `yaml:"global_min_gap_seconds" json:"global_min_gap_seconds"` // min gap across cameras (FR-DET-8)
	BaseURL             string `yaml:"base_url" json:"base_url"`                             // reachable UI origin for deep links (FR-NOT-2)
	QuietStart          string `yaml:"quiet_hours.start" json:"quiet_hours_start"`
	QuietEnd            string `yaml:"quiet_hours.end" json:"quiet_hours_end"`
}

// Custom unmarshal for the inline quiet_hours mapping.
func (n *Notifications) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Provider            string `yaml:"provider" json:"provider"`
		CooldownSeconds     int    `yaml:"cooldown_seconds" json:"cooldown_seconds"`
		GlobalMinGapSeconds int    `yaml:"global_min_gap_seconds" json:"global_min_gap_seconds"`
		BaseURL             string `yaml:"base_url" json:"base_url"`
		QuietHours          struct {
			Start string `yaml:"start" json:"start"`
			End   string `yaml:"end" json:"end"`
		} `yaml:"quiet_hours"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	n.Provider = raw.Provider
	n.CooldownSeconds = raw.CooldownSeconds
	n.GlobalMinGapSeconds = raw.GlobalMinGapSeconds
	n.BaseURL = raw.BaseURL
	n.QuietStart = raw.QuietHours.Start
	n.QuietEnd = raw.QuietHours.End
	return nil
}

type API struct {
	Bind string `yaml:"bind" json:"bind"`
	Auth string `yaml:"auth" json:"auth"`
}

type Remote struct {
	Tailscale bool `yaml:"tailscale" json:"tailscale"`
}

// Finalize applies defaults and validates an in-memory config — used by
// the PUT /api/config path (FR-API-4) before anything touches disk.
func (c *Config) Finalize() error {
	c.applyDefaults()
	if errs := c.Validate(); len(errs) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}

// WithPlaceholdersResolved returns a deep copy with every ${secret:key}
// reference replaced by a benign dummy value, so a config round-tripped
// through the redacted API view can pass URL validation before secrets
// resolve on reload.
func (c *Config) WithPlaceholdersResolved() *Config {
	raw, err := json.Marshal(c)
	if err != nil {
		return c
	}
	var dup Config
	if json.Unmarshal(raw, &dup) != nil {
		return c
	}
	walkStrings(reflect.ValueOf(&dup).Elem(), sanitizePlaceholders)
	return &dup
}

func sanitizePlaceholders(s string) string {
	for {
		i := strings.Index(s, SecretPrefix)
		if i < 0 {
			return s
		}
		rest := s[i+len(SecretPrefix):]
		j := strings.Index(rest, "}")
		if j < 0 {
			return s
		}
		s = s[:i] + "ph" + s[i+len(SecretPrefix)+j+1:]
	}
}

// MarshalYAML renders quiet hours inline (matching the unmarshal shape).
func (n Notifications) MarshalYAML() (any, error) {
	return map[string]any{
		"provider":               n.Provider,
		"cooldown_seconds":       n.CooldownSeconds,
		"global_min_gap_seconds": n.GlobalMinGapSeconds,
		"base_url":               n.BaseURL,
		"quiet_hours": map[string]string{
			"start": n.QuietStart,
			"end":   n.QuietEnd,
		},
	}, nil
}

// Load reads and validates config.yaml with secrets.yaml interpolation.
// dir is the directory containing both files. Returns an error listing all
// validation problems (FR-CFG-3).
func Load(dir string) (*Config, error) {
	cfg, _, err := LoadEx(dir)
	return cfg, err
}

// LoadEx also returns redaction templates: field-path → the original
// (pre-interpolation) string containing ${secret:key} references. This is
// the redacted view for GET /api/config — path-keyed, so two fields may
// share the same secret VALUE without ambiguity.
func LoadEx(dir string) (*Config, map[string]string, error) {
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}

	secrets, err := loadSecrets(filepath.Join(dir, "secrets.yaml"))
	if err != nil {
		return nil, nil, err
	}
	templates := map[string]string{}
	if err := interpolateWithTemplates(&cfg, secrets, templates); err != nil {
		return nil, nil, err
	}
	for i := range cfg.Cameras {
		c := &cfg.Cameras[i]
		c.SourceMain = escapeURLCreds(c.SourceMain)
		c.SourceSub = escapeURLCreds(c.SourceSub)
		c.MainURL = escapeURLCreds(c.MainURL)
		c.SubURL = escapeURLCreds(c.SubURL)
	}

	cfg.applyDefaults()
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, nil, fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
	}
	return &cfg, templates, nil
}

// DeepCopy returns a JSON-round-tripped copy of the config.
func (c *Config) DeepCopy() *Config {
	raw, err := json.Marshal(c)
	if err != nil {
		return c
	}
	var dup Config
	if json.Unmarshal(raw, &dup) != nil {
		return c
	}
	return &dup
}

// LoadSecrets exposes secrets.yaml to callers that need secret values
// outside config strings (MediaMTX viewer pass, API token). Never log them.
func LoadSecrets(path string) (map[string]string, error) {
	return loadSecrets(path)
}

// escapeURLCreds percent-escapes interpolated userinfo so passwords with
// @ : / ? # do not corrupt the authority component. Idempotent: URLs that
// already parse with the expected host are returned unchanged.
func escapeURLCreds(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	scheme, rest := u[:i+3], u[i+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return u
	}
	// The authority ends at the first '/' after the last '@' — earlier
	// slashes may belong to the password itself.
	userinfo := rest[:at]
	tail := rest[at+1:]
	host, pathPart := tail, ""
	if slash := strings.Index(tail, "/"); slash >= 0 {
		host, pathPart = tail[:slash], tail[slash:]
	}
	if p, err := url.Parse(u); err == nil && p.Host == host {
		return u // already well-formed
	}
	sep := strings.Index(userinfo, ":")
	if sep < 0 {
		return scheme + url.User(userinfo).String() + "@" + host + pathPart
	}
	return scheme + url.UserPassword(userinfo[:sep], userinfo[sep+1:]).String() + "@" + host + pathPart
}

func loadSecrets(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	if st, statErr := os.Stat(path); statErr == nil && st.Mode().Perm() != 0o600 {
		fmt.Fprintf(os.Stderr, "warning: %s should have 0600 permissions (FR-CFG-2), has %o\n", path, st.Mode().Perm())
	}
	var m map[string]string
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	return m, nil
}

// interpolate walks every string field and substitutes ${secret:key} refs.
func interpolate(cfg *Config, secrets map[string]string) error {
	return interpolateWithTemplates(cfg, secrets, nil)
}

// interpolateWithTemplates substitutes refs and records, per field path,
// the ORIGINAL string whenever a substitution occurred. That original is
// exactly the redacted form for the field.
func interpolateWithTemplates(cfg *Config, secrets map[string]string, templates map[string]string) error {
	var errs []string
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		if strings.HasPrefix(path, ".") {
			path = path[1:]
		}
		switch v.Kind() {
		case reflect.String:
			orig := v.String()
			if strings.Contains(orig, SecretPrefix) && templates != nil && path != "" {
				templates[path] = orig
			}
			v.SetString(replaceSecrets(orig, secrets, &errs))
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+t.Field(i).Name)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), fmt.Sprintf("%s.%d", path, i))
			}
		case reflect.Ptr, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		}
	}
	walk(reflect.ValueOf(cfg).Elem(), "")
	if len(errs) > 0 {
		return fmt.Errorf("secrets: %s", strings.Join(errs, "; "))
	}
	return nil
}

func replaceSecrets(s string, secrets map[string]string, errs *[]string) string {
	for {
		i := strings.Index(s, SecretPrefix)
		if i < 0 {
			return s
		}
		rest := s[i+len(SecretPrefix):]
		j := strings.Index(rest, "}")
		if j < 0 {
			*errs = append(*errs, "unterminated secret reference")
			return s
		}
		key := rest[:j]
		val, ok := secrets[key]
		if !ok {
			*errs = append(*errs, fmt.Sprintf("unknown secret %q", key))
			return s
		}
		s = s[:i] + val + s[i+len(SecretPrefix)+j+1:]
	}
}

func walkStrings(v reflect.Value, f func(string) string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(f(v.String()))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkStrings(v.Field(i), f)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), f)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), f)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkStrings(v.MapIndex(k), f)
		}
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), f)
		}
	}
}

// Redactor renders the redacted view of a config using field-path
// templates captured at load time (see LoadEx). Path-keyed, so identical
// secret VALUES in different fields never cross-contaminate (FLAG-13).
type Redactor struct {
	templates map[string]string
}

func NewRedactorFromTemplates(templates map[string]string) *Redactor {
	return &Redactor{templates: templates}
}

// RedactedCopy returns a deep copy of cfg with every field that referenced
// secrets restored to its ${secret:key} template form.
func (r *Redactor) RedactedCopy(cfg *Config) *Config {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var dup Config
	if json.Unmarshal(raw, &dup) != nil {
		return cfg
	}
	if r == nil || r.templates == nil {
		return &dup
	}
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		if strings.HasPrefix(path, ".") {
			path = path[1:]
		}
		switch v.Kind() {
		case reflect.String:
			if t, ok := r.templates[path]; ok && path != "" {
				v.SetString(t)
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+t.Field(i).Name)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), fmt.Sprintf("%s.%d", path, i))
			}
		case reflect.Ptr, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		}
	}
	walk(reflect.ValueOf(&dup).Elem(), "")
	return &dup
}
