// Package notify is COMP-5 (FR-NOT-1..5): ntfy/Telegram providers,
// per-camera + global cooldowns, quiet hours, and a retry queue that never
// blocks detection (FR-NOT-4). Delivery success marks the event notified
// in the store so unnotified events replay after restart.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync"
	"time"

	"pocketnvr/internal/config"
)

// Item is one notification payload (from an event).
type Item struct {
	EventID  int64
	CameraID string
	Camera   string // human name
	Score    float64
	StartTS  int64 // epoch ms
	Snapshot []byte
	DeepLink string // UI URL to the event (FR-NOT-2)
}

// Notifier delivers to one provider.
type Notifier interface {
	Send(ctx context.Context, it Item) error
	Name() string
}

type clock struct{ now func() time.Time }

// Service serializes deliveries with cooldowns/quiet hours and retries.
type Service struct {
	mu        sync.Mutex
	lastPerCa map[string]time.Time
	lastAny   time.Time

	cfg       func() *config.Config
	token     func(key string) string
	providers []Notifier
	store     NotifiedMarker
	logger    *slog.Logger
	client    *http.Client
	q         chan Item
	nowFn     func() time.Time
}

type NotifiedMarker interface {
	MarkNotified(id int64) error
}

// New builds a service; cfg/token are indirections so SIGHUP reloads apply.
func New(cfg func() *config.Config, token func(string) string,
	marker NotifiedMarker, logger *slog.Logger) *Service {
	s := &Service{
		lastPerCa: map[string]time.Time{},
		cfg:       cfg,
		token:     token,
		store:     marker,
		logger:    logger,
		client:    &http.Client{Timeout: 15 * time.Second},
		q:         make(chan Item, 256),
		nowFn:     time.Now,
	}
	s.rebuildProviders()
	return s
}

func (s *Service) rebuildProviders() {
	c := s.cfg()
	var ps []Notifier
	if c.Notifications.Provider == "ntfy" || c.Notifications.Provider == "both" {
		if url := s.token("ntfy_topic_url"); url != "" {
			ps = append(ps, &ntfy{url: url, token: s.token("ntfy_token"), client: s.client})
		}
	}
	if c.Notifications.Provider == "telegram" || c.Notifications.Provider == "both" {
		if tok := s.token("telegram_bot_token"); tok != "" && s.token("telegram_chat_id") != "" {
			ps = append(ps, &telegram{token: tok, chatID: s.token("telegram_chat_id"), client: s.client})
		}
	}
	s.providers = ps
	if len(ps) == 0 {
		s.logger.Warn("no notification provider configured (check secrets + notifications.provider)")
	}
}

// Reload re-reads provider config (SIGHUP path).
func (s *Service) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildProviders()
}

// allowed applies quiet hours + cooldowns (FR-DET-8, FR-NOT-3/5).
func (s *Service) allowed(camID string, now time.Time) (bool, string) {
	c := s.cfg()
	if m := config.QuietMinutes(c.Notifications.QuietStart); m >= 0 {
		if inQuietHours(now, m, config.QuietMinutes(c.Notifications.QuietEnd)) {
			return false, "quiet hours"
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastPerCa[camID]; ok {
		if since := now.Sub(last); since < time.Duration(c.Notifications.CooldownSeconds)*time.Second {
			return false, fmt.Sprintf("camera cooldown (%s left)", since.Round(time.Second))
		}
	}
	gap := c.Notifications.GlobalMinGapSeconds
	if gap > 0 && !s.lastAny.IsZero() {
		if since := now.Sub(s.lastAny); since < time.Duration(gap)*time.Second {
			return false, fmt.Sprintf("global min-gap (%s left)", since.Round(time.Second))
		}
	}
	s.lastPerCa[camID] = now
	s.lastAny = now
	return true, ""
}

// inQuietHours handles windows crossing midnight.
func inQuietHours(now time.Time, startMin, endMin int) bool {
	cur := now.Hour()*60 + now.Minute()
	if startMin == endMin {
		return false
	}
	if startMin < endMin {
		return cur >= startMin && cur < endMin
	}
	return cur >= startMin || cur < endMin // crosses midnight
}

// Enqueue evaluates cooldown/quiet hours and queues for delivery. Never
// blocks the caller (detector path) beyond the channel offer (FR-NOT-4).
// Suppressed events (quiet hours, cooldown, or no provider configured) are
// still stored — the caller persisted them already; suppression only skips
// the push, and is marked to avoid replay.
func (s *Service) Enqueue(it Item) (delivered bool, reason string) {
	if !s.hasProviders() {
		if err := s.store.MarkNotified(it.EventID); err != nil {
			s.logger.Error("mark event (no providers)", "err", err)
		}
		return false, "no provider configured"
	}
	ok, why := s.allowed(it.CameraID, s.nowFn())
	if !ok {
		if err := s.store.MarkNotified(it.EventID); err != nil {
			s.logger.Error("mark suppressed event", "err", err)
		}
		return false, why
	}
	select {
	case s.q <- it:
		return true, ""
	default:
		return false, "notification queue full"
	}
}

func (s *Service) hasProviders() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.providers) > 0
}

// Run drains the queue with retries until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-s.q:
			for attempt := 0; ; attempt++ {
				if err := s.deliver(ctx, it); err == nil {
					_ = s.store.MarkNotified(it.EventID)
					break
				} else if ctx.Err() != nil {
					return
				} else {
					wait := backoff[min(attempt, len(backoff)-1)]
					s.logger.Warn("notify failed, will retry",
						"event", it.EventID, "attempt", attempt+1, "retry_in", wait.String(), "err", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
				}
			}
		}
	}
}

// deliver pushes to every configured provider.
func (s *Service) deliver(ctx context.Context, it Item) error {
	s.mu.Lock()
	providers := append([]Notifier(nil), s.providers...)
	s.mu.Unlock()
	if len(providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	var errs []string
	for _, p := range providers {
		if err := p.Send(ctx, it); err != nil {
			errs = append(errs, p.Name()+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", join(errs))
	}
	return nil
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}

func caption(it Item) string {
	msg := fmt.Sprintf("%s — person detected (%.0f%%)", it.Camera, it.Score*100)
	t := time.UnixMilli(it.StartTS).Format("15:04:05")
	msg += " at " + t
	if it.DeepLink != "" {
		msg += "\n" + it.DeepLink
	}
	return msg
}

// --- ntfy ---

type ntfy struct {
	url    string
	token  string
	client *http.Client
}

func (n *ntfy) Name() string { return "ntfy" }

func (n *ntfy) Send(ctx context.Context, it Item) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, n.url, bytes.NewReader(it.Snapshot))
	if err != nil || n.url == "" {
		return fmt.Errorf("bad ntfy url")
	}
	req.Header.Set("Title", "PocketNVR — "+it.Camera)
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "walking")
	req.Header.Set("Message", caption(it))
	req.Header.Set("Filename", "event-"+strconv.FormatInt(it.EventID, 10)+".jpg")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy status %d", resp.StatusCode)
	}
	return nil
}

// --- telegram ---

type telegram struct {
	token  string
	chatID string
	client *http.Client
}

func (t *telegram) Name() string { return "telegram" }

func (t *telegram) Send(ctx context.Context, it Item) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", t.chatID)
	_ = mw.WriteField("caption", caption(it))
	if len(it.Snapshot) > 0 {
		fw, err := mw.CreateFormFile("photo", "event.jpg")
		if err == nil {
			_, _ = fw.Write(it.Snapshot)
		}
	}
	_ = mw.Close()

	endpoint := "https://api.telegram.org/bot" + t.token + "/sendPhoto"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status %d", resp.StatusCode)
	}
	return nil
}
