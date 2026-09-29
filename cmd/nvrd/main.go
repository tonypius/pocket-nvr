// nvrd is the PocketNVR Go daemon: event store + notifications + local API
// + static UI + retention (COMP-4/5/6 merged per FRS §3.1).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"pocketnvr/internal/api"
	"pocketnvr/internal/clips"
	"pocketnvr/internal/config"
	"pocketnvr/internal/detectorcfg"
	"pocketnvr/internal/mediamtx"
	"pocketnvr/internal/notify"
	"pocketnvr/internal/retention"
	"pocketnvr/internal/store"
)

func main() {
	var (
		cfgDir      = flag.String("config", envOr("NVR_CONFIG_DIR", defaultConfigDir()), "directory containing config.yaml and secrets.yaml")
		check       = flag.Bool("check", false, "validate config and exit")
		mediaOut    = flag.String("mediamtx-config", "", "generate mediamtx.yml at path and exit")
		detectorOut = flag.String("detector-config", "", "generate detector.json at path and exit")
		showVer     = flag.Bool("version", false, "print version")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("nvrd", api.Version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("nvrd starting", "version", api.Version, "config_dir", *cfgDir)

	cfg, redactTemplates, err := config.LoadEx(*cfgDir)
	if err != nil {
		logger.Error("config invalid", "err", err)
		os.Exit(1)
	}

	if *check {
		fmt.Println("config OK")
		return
	}

	if *mediaOut != "" {
		if err := writeMediaConfig(cfg, *cfgDir, *mediaOut, nil); err != nil {
			logger.Error("generate mediamtx config", "err", err)
			os.Exit(1)
		}
		logger.Info("mediamtx.yml written", "path", *mediaOut)
		return
	}

	if *detectorOut != "" {
		if err := writeDetectorConfig(cfg, *cfgDir, *detectorOut); err != nil {
			logger.Error("generate detector config", "err", err)
			os.Exit(1)
		}
		logger.Info("detector.json written", "path", *detectorOut)
		return
	}

	if err := os.MkdirAll(cfg.System.BasePath, 0o755); err != nil {
		logger.Error("base_path", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(cfg.System.BasePath, "events.db"))
	if err != nil {
		logger.Error("store open", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	type camRow = struct {
		ID      string
		Name    string
		Enabled bool
	}
	rows := make([]camRow, 0, len(cfg.Cameras))
	for _, c := range cfg.Cameras {
		rows = append(rows, camRow{ID: c.ID, Name: c.Name, Enabled: c.IsEnabled()})
	}
	if err := st.SeedCameras(rows); err != nil {
		logger.Error("seed cameras", "err", err)
		os.Exit(1)
	}

	secrets, err := config.LoadSecrets(filepath.Join(*cfgDir, "secrets.yaml"))
	if err != nil {
		logger.Error("secrets", "err", err)
		os.Exit(1)
	}
	apiToken := secrets["api_token"]
	if apiToken == "" {
		logger.Error("secrets.yaml: api_token is required (FR-API-5: no anonymous access)")
		os.Exit(1)
	}

	redactor := config.NewRedactorFromTemplates(redactTemplates)
	srv := api.New(cfg, redactor, apiToken, st, logger, uiDir())
	srv.SetDirs(*cfgDir)
	srv.SetHupSelf(func() { _ = syscall.Kill(os.Getpid(), syscall.SIGHUP) })

	// COMP-5 + FR-EVT-3: detector events → notifications + clip extraction.
	notifier := notify.New(cfgGet(&cfg), keyGet(&secrets), st, logger)
	extractor := clips.New(cfgGet(&cfg), st, logger, ffmpegPath())
	srv.SetEventHandler(func(cameraID, cameraName string, eventID, startTS, endTS int64, score float64, snapshot []byte) {
		link := deepLink(cfg, secrets["api_token"], eventID)
		delivered, why := notifier.Enqueue(notify.Item{
			EventID: eventID, CameraID: cameraID, Camera: cameraName,
			Score: score, StartTS: startTS, Snapshot: snapshot, DeepLink: link,
		})
		logger.Info("notification queued", "event", eventID, "delivered", delivered, "reason", why)
		if endTS > 0 {
			extractor.Enqueue(clips.Job{EventID: eventID, CameraID: cameraID,
				StartTS: startTS, EndTS: endTS})
		}
	})
	srv.SetMtxProbe(mtxProbe(cfg.Media.RTSPAddress))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go notifier.Run(ctx)
	stopClips := make(chan struct{})
	go extractor.Run(stopClips)

	// FR-NOT-4: events that never delivered (offline provider, restart)
	// replay through the queue on boot.
	go func() {
		pending, err := st.Unnotified(100)
		if err != nil {
			logger.Warn("unnotified replay", "err", err)
			return
		}
		for _, e := range pending {
			cameraName := e.CameraID
			for _, c := range cfg.Cameras {
				if c.ID == e.CameraID {
					cameraName = c.Name
					break
				}
			}
			notifier.Enqueue(notify.Item{
				EventID: e.ID, CameraID: e.CameraID, Camera: cameraName,
				Score: e.Score, StartTS: e.StartTS,
				DeepLink: deepLink(cfg, secrets["api_token"], e.ID),
			})
		}
		if len(pending) > 0 {
			logger.Info("replaying unnotified events", "count", len(pending))
		}
	}()

	go retention.Run(ctx, st, cfgGet(&cfg), logger, 10*time.Minute)

	// Free-space guard (FR-SUP-8): below min free → prune now; if still
	// low → pause continuous recording by reloading MediaMTX with
	// record: no (restream + detection keep running); resume with
	// hysteresis once space recovers. A failed pause/resume (config write
	// or SIGHUP) is retried on the next tick — latching the state without
	// reloading MediaMTX would leave recording running into a full disk.
	off := false
	on := true
	// recOverride carries the guard's recording override into config
	// reloads, so a SIGHUP or Settings save can't silently resume recording
	// while the guard has it paused. nil = follow config recording.mode.
	var recOverride atomic.Pointer[bool]
	go func() {
		paused := false
		base := filepath.Dir(*cfgDir) // config dir == base on the device
		if cfg.System.BasePath != "" {
			base = cfg.System.BasePath
		}
		for {
			minBytes := uint64(cfg.System.StorageMinFreeGB) * 1e9
			free, ok := api.FreeDiskBytes(base)
			if ok {
				if !paused && free < minBytes {
					retention.Pass(st, cfg, logger)
					if free2, ok2 := api.FreeDiskBytes(base); !ok2 || free2 < minBytes {
						logger.Warn("storage low: pausing continuous recording (FR-SUP-8)",
							"free_gb", free/1e9)
						if err := writeMediaConfig(cfg, *cfgDir, filepath.Join(base, "mediamtx.yml"), &off); err != nil {
							logger.Error("pause failed writing mediamtx.yml; retrying next tick", "err", err)
						} else if err := hupProcess("mediamtx", base); err != nil {
							logger.Error("pause failed signaling mediamtx; retrying next tick", "err", err)
						} else {
							recOverride.Store(&off)
							srv.SetStorageDegraded(true)
							paused = true
						}
					}
				} else if paused && free > minBytes+1e9 { // 1 GB hysteresis
					logger.Info("storage recovered: resuming recording")
					if err := writeMediaConfig(cfg, *cfgDir, filepath.Join(base, "mediamtx.yml"), &on); err != nil {
						logger.Error("resume failed writing mediamtx.yml; retrying next tick", "err", err)
					} else if err := hupProcess("mediamtx", base); err != nil {
						logger.Error("resume failed signaling mediamtx; retrying next tick", "err", err)
					} else {
						recOverride.Store(nil)
						srv.SetStorageDegraded(false)
						paused = false
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()

	addr := cfg.API.Bind
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("bind", "addr", addr, "err", err)
		os.Exit(1)
	}
	httpServer := &http.Server{Handler: srv.Handler()}
	go func() {
		logger.Info("api listening", "addr", addr)
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("http serve", "err", err)
			os.Exit(1)
		}
	}()

	// SIGHUP: reload config; on invalid config keep the previous good state
	// (FR-CFG-3 / FR-DET-11).
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	for s := range sig {
		if s == syscall.SIGHUP {
			if nc, nrt, err := config.LoadEx(*cfgDir); err != nil {
				logger.Error("reload refused (keeping previous config)", "err", err)
			} else {
				cfg = nc
				srv.SwapConfig(cfg, config.NewRedactorFromTemplates(nrt))
				notifier.Reload()
				// regenerate downstream runtime configs (FR-CFG-1) and nudge
				// MediaMTX; the detector re-execs on its own SIGHUP. The
				// current record override (guard pause) is preserved.
				if err := writeMediaConfig(cfg, *cfgDir, filepath.Join(baseOf(cfg), "mediamtx.yml"), recOverride.Load()); err != nil {
					logger.Error("regen mediamtx.yml", "err", err)
				} else if err := hupProcess("mediamtx", baseOf(cfg)); err != nil {
					logger.Warn("reload mediamtx", "err", err)
				}
				if err := writeDetectorConfig(cfg, *cfgDir, filepath.Join(baseOf(cfg), "detector.json")); err != nil {
					logger.Error("regen detector.json", "err", err)
				}
				logger.Info("config reloaded")
			}
			continue
		}
		logger.Info("shutting down", "signal", s.String())
		cancel()
		_ = httpServer.Close()
		return
	}
}

// baseOf resolves the appliance base path (config dir on device, else the
// configured base_path).
func baseOf(cfg *config.Config) string {
	if cfg.System.BasePath == "/data/nvr" {
		return cfg.System.BasePath
	}
	if _, err := os.Stat("/data/nvr/config.yaml"); err == nil {
		return "/data/nvr"
	}
	return cfg.System.BasePath
}

// cfgGet / keyGet give subsystems a live view of the current config and
// secrets without leaking the pointers around main.
func cfgGet(p **config.Config) func() *config.Config {
	return func() *config.Config { return *p }
}

func keyGet(m *map[string]string) func(string) string {
	return func(k string) string { return (*m)[k] }
}

// deepLink builds the tappable event URL for pushes (FR-NOT-2). The API
// token is embedded because UI assets load via ?token=; acceptable for a
// single-user, Tailscale-scoped appliance (NFR-8).
func deepLink(cfg *config.Config, token string, eventID int64) string {
	if cfg.Notifications.BaseURL == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("%s/api/events/%d?token=%s", cfg.Notifications.BaseURL, eventID, token)
}

// ffmpegPath prefers the bundled static binary (phone); falls back to PATH
// (dev machines).
func ffmpegPath() string {
	for _, p := range []string{"/data/nvr/bin/ffmpeg", "ffmpeg"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if p == "ffmpeg" {
			if _, err := exec.LookPath("ffmpeg"); err == nil {
				return "ffmpeg"
			}
		}
	}
	return "ffmpeg"
}

// mtxProbe checks the MediaMTX RTSP listener every call (cheap TCP dial).
func mtxProbe(rtspAddr string) func() bool {
	return func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1"+rtspAddr, 2*time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}

func writeMediaConfig(cfg *config.Config, cfgDir, out string, recordOverride *bool) error {
	secrets, err := config.LoadSecrets(filepath.Join(cfgDir, "secrets.yaml"))
	if err != nil {
		return err
	}
	b, err := mediamtx.Generate(cfg, secrets["mediamtx_viewer_pass"], recordOverride)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o600)
}

func writeDetectorConfig(cfg *config.Config, cfgDir, out string) error {
	secrets, err := config.LoadSecrets(filepath.Join(cfgDir, "secrets.yaml"))
	if err != nil {
		return err
	}
	b, err := detectorcfg.Generate(cfg, secrets["api_token"], cfg.System.BasePath)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o600)
}

// hupProcess sends SIGHUP to a daemon managed by nvrctl (pidfile-based).
// An error means no signal was delivered (missing/bad pidfile, dead process)
// — callers must treat the reload as not having happened.
func hupProcess(name, base string) error {
	b, err := os.ReadFile(filepath.Join(base, "run", name+".pid"))
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("bad pidfile for %s", name)
	}
	return syscall.Kill(pid, syscall.SIGHUP)
}

func defaultConfigDir() string {
	if _, err := os.Stat("/data/nvr/config.yaml"); err == nil {
		return "/data/nvr"
	}
	return "."
}

// uiDir resolves the static UI directory (env override for dev).
// uiDir resolves the static UI directory: env override, device path, or
// relative (dev).
func uiDir() string {
	if v := os.Getenv("NVR_UI_DIR"); v != "" {
		return v
	}
	if _, err := os.Stat("/data/nvr/ui/index.html"); err == nil {
		return "/data/nvr/ui"
	}
	return "ui"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
