package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"gopkg.in/yaml.v3"

	"pocketnvr/internal/config"
	"pocketnvr/internal/store"
)

// PushStore is the subscription storage the Web Push provider needs.
type PushStore interface {
	ListPushSubs() ([]store.PushSub, error)
	DeletePushSub(endpoint string) error
	MarkPushOK(endpoint string) error
}

// pushPayload is the JSON the service worker receives (encrypted end to end
// to the device, RFC 8291 — the push service cannot read it).
type pushPayload struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	EventID int64  `json:"event_id,omitempty"`
	Camera  string `json:"camera,omitempty"`
	URL     string `json:"url"`             // in-app route, e.g. #/events/42
	Image   string `json:"image,omitempty"` // snapshot (Android shows it; iOS ignores)
	Tag     string `json:"tag"`
	TS      int64  `json:"ts"`
}

// pushResult tallies one broadcast.
type pushResult struct {
	LastErr   string // cause of the most recent transport failure (no URLs)
	Sent      int    // accepted by the push service
	Gone      int    // subscription expired/unsubscribed (404/410) → removed
	Dropped   int    // permanent client error (bad VAPID, payload) → not retried
	Retriable int    // network error / 429 / 5xx → worth retrying
}

// webPush delivers to every stored subscription. It is always registered once
// VAPID keys exist: subscribing a device is the opt-in, so it is independent
// of the ntfy/telegram `provider` setting.
type webPush struct {
	store   PushStore
	key     func(string) string
	subject func() string
	client  *http.Client
	logger  *slog.Logger
}

func (w *webPush) Name() string { return "webpush" }

// Send implements Notifier. It succeeds when at least one device accepted the
// push, or when nothing is retriable; it errors only when every attempt hit a
// transient failure, so the service retries without re-notifying devices that
// already got the alert.
func (w *webPush) Send(ctx context.Context, it Item) error {
	res, err := w.broadcast(ctx, payloadFor(it))
	if err != nil {
		return err
	}
	if res.Sent == 0 && res.Retriable > 0 {
		return fmt.Errorf("webpush: %d transient failure(s), none delivered", res.Retriable)
	}
	return nil
}

func (w *webPush) broadcast(ctx context.Context, p pushPayload) (pushResult, error) {
	var res pushResult
	pub, priv := w.key("vapid_public"), w.key("vapid_private")
	if pub == "" || priv == "" {
		return res, fmt.Errorf("webpush: VAPID keys not configured")
	}
	subs, err := w.store.ListPushSubs()
	if err != nil {
		return res, err
	}
	if len(subs) == 0 {
		return res, nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return res, err
	}
	for _, sub := range subs {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resp, err := webpush.SendNotificationWithContext(sctx, body, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, &webpush.Options{
			HTTPClient:      w.client,
			Subscriber:      w.subject(),
			VAPIDPublicKey:  pub,
			VAPIDPrivateKey: priv,
			TTL:             3600,
			Urgency:         webpush.UrgencyHigh,
		})
		cancel()
		if err != nil {
			// *url.Error embeds the full endpoint URL, which is a bearer
			// capability for that device — keep only the cause.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			w.logger.Warn("webpush send failed", "err", err)
			res.LastErr = err.Error()
			res.Retriable++
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			res.Sent++
			_ = w.store.MarkPushOK(sub.Endpoint)
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			res.Gone++
			_ = w.store.DeletePushSub(sub.Endpoint)
			w.logger.Info("webpush subscription expired; removed")
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			res.Retriable++
			w.logger.Warn("webpush push service busy", "status", resp.StatusCode)
		default:
			res.Dropped++
			w.logger.Warn("webpush rejected (not retrying)", "status", resp.StatusCode)
		}
	}
	return res, nil
}

func payloadFor(it Item) pushPayload {
	p := pushPayload{
		Title:   "PocketNVR — " + it.Camera,
		Body:    fmt.Sprintf("%s detected (%.0f%%) at %s", titleCase(labelOrPerson(it.Label)), it.Score*100, time.UnixMilli(it.StartTS).Format("15:04:05")),
		EventID: it.EventID,
		Camera:  it.CameraID,
		URL:     "#/events/" + strconv.FormatInt(it.EventID, 10),
		Tag:     "event-" + strconv.FormatInt(it.EventID, 10),
		TS:      it.StartTS,
	}
	// The deep link (set only when notifications.base_url is configured) is
	// <base>/api/events/<id>?token=…; the snapshot sits one path segment over.
	// It travels inside the encrypted payload.
	marker := "/api/events/" + strconv.FormatInt(it.EventID, 10) + "?"
	if i := strings.Index(it.DeepLink, marker); i >= 0 {
		p.Image = it.DeepLink[:i] + "/api/events/" + strconv.FormatInt(it.EventID, 10) + "/snapshot?" + it.DeepLink[i+len(marker):]
	}
	return p
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SetPushStore enables Web Push delivery (once VAPID keys exist).
func (s *Service) SetPushStore(ps PushStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.push = ps
	s.rebuildProviders()
}

// PushTest sends a test notification to every subscribed device, bypassing
// cooldowns and quiet hours. It reports how many devices accepted it.
func (s *Service) PushTest(ctx context.Context) (int, error) {
	s.mu.Lock()
	w := s.newWebPush()
	s.mu.Unlock()
	if w == nil {
		return 0, fmt.Errorf("web push is not enabled")
	}
	res, err := w.broadcast(ctx, pushPayload{
		Title: "PocketNVR test",
		Body:  "Push notifications are working on this device.",
		URL:   "#/settings",
		Tag:   "pocketnvr-test",
		TS:    time.Now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	if res.Sent == 0 && res.Retriable > 0 {
		cause := res.LastErr
		if cause == "" {
			cause = "push service busy"
		}
		return 0, fmt.Errorf("could not reach the push service: %s", cause)
	}
	if res.Sent == 0 && res.Dropped > 0 {
		return 0, fmt.Errorf("the push service rejected the notification")
	}
	return res.Sent, nil
}

// newWebPush builds the provider if a store is wired and keys exist.
// Callers hold s.mu.
func (s *Service) newWebPush() *webPush {
	if s.push == nil || s.token("vapid_public") == "" || s.token("vapid_private") == "" {
		return nil
	}
	return &webPush{
		store: s.push,
		key:   s.token,
		subject: func() string {
			if b := s.cfg().Notifications.BaseURL; strings.HasPrefix(b, "https://") {
				return b
			}
			// VAPID requires a contact URI; push services only check its shape.
			return "mailto:pocketnvr@example.com"
		},
		client: s.client,
		logger: s.logger,
	}
}

// EnsureVAPID makes sure secrets.yaml holds a VAPID key pair for Web Push,
// generating one on first run. It returns the secrets map to use afterwards.
// The pair identifies this server to push services; losing it just means
// devices have to re-subscribe.
func EnsureVAPID(path string, secrets map[string]string) (map[string]string, error) {
	if secrets["vapid_public"] != "" && secrets["vapid_private"] != "" {
		return secrets, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return secrets, fmt.Errorf("generate VAPID keys: %w", err)
	}
	cur, err := config.LoadSecrets(path)
	if err != nil {
		return secrets, err
	}
	if cur == nil {
		cur = map[string]string{}
	}
	cur["vapid_public"], cur["vapid_private"] = pub, priv
	out, err := yaml.Marshal(cur)
	if err != nil {
		return secrets, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return secrets, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return secrets, err
	}
	merged := map[string]string{}
	for k, v := range secrets {
		merged[k] = v
	}
	merged["vapid_public"], merged["vapid_private"] = pub, priv
	return merged, nil
}
