package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func pushKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	rand.Read(a)
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(a)
}

func postJSON(t *testing.T, url, token, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Api-Token", token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return r, readAll(r)
}

func subBody(endpoint, p256dh, auth string) string {
	return fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`, endpoint, p256dh, auth)
}

func TestPushVAPIDKeyAndAuth(t *testing.T) {
	srv, ts := setup(t)
	if r, _ := get(t, ts.URL+"/api/push/vapid-key", "sekrit"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("push not wired yet → 503, got %d", r.StatusCode)
	}
	srv.SetPush(func() string { return "PUBKEY" }, nil)
	if r, _ := get(t, ts.URL+"/api/push/vapid-key", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("vapid-key needs the API token, got %d", r.StatusCode)
	}
	r, body := get(t, ts.URL+"/api/push/vapid-key", "sekrit")
	if r.StatusCode != 200 || !strings.Contains(string(body), `"public_key": "PUBKEY"`) {
		t.Fatalf("vapid-key: %d %s", r.StatusCode, body)
	}
	// subscribing is not anonymous either
	p, a := pushKeys(t)
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "", subBody("https://fcm.googleapis.com/x", p, a)); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("subscribe without token → 401, got %d", r.StatusCode)
	}
}

func TestPushSubscribeLifecycle(t *testing.T) {
	srv, ts := setup(t)
	srv.SetPush(func() string { return "PUBKEY" }, func(context.Context) (int, error) { return 1, nil })
	p, a := pushKeys(t)
	ep := "https://fcm.googleapis.com/fcm/send/abc"

	r, body := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(ep, p, a))
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", r.StatusCode, body)
	}
	// idempotent: re-subscribing the same device keeps one row
	postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(ep, p, a))
	if n, _ := srv.store.CountPushSubs(); n != 1 {
		t.Fatalf("want 1 subscription, got %d", n)
	}
	_, st := get(t, ts.URL+"/api/push/status", "sekrit")
	var status struct {
		Enabled bool `json:"enabled"`
		Devices int  `json:"devices"`
	}
	if err := json.Unmarshal(st, &status); err != nil || !status.Enabled || status.Devices != 1 {
		t.Fatalf("status = %s (%v)", st, err)
	}

	r, body = postJSON(t, ts.URL+"/api/push/test", "sekrit", "{}")
	if r.StatusCode != 200 || !strings.Contains(body, `"sent": 1`) {
		t.Fatalf("test push: %d %s", r.StatusCode, body)
	}

	if r, _ := postJSON(t, ts.URL+"/api/push/unsubscribe", "sekrit", fmt.Sprintf(`{"endpoint":%q}`, ep)); r.StatusCode != 200 {
		t.Fatalf("unsubscribe: %d", r.StatusCode)
	}
	if n, _ := srv.store.CountPushSubs(); n != 0 {
		t.Fatalf("want 0 subscriptions, got %d", n)
	}
}

func TestPushSubscribeRejectsBadInput(t *testing.T) {
	srv, ts := setup(t)
	srv.SetPush(func() string { return "PUBKEY" }, nil)
	p, a := pushKeys(t)
	for name, ep := range map[string]string{
		"plain http":  "http://fcm.googleapis.com/x",
		"loopback IP": "https://127.0.0.1/x",
		"LAN IP":      "https://192.168.0.1/x",
		"no dot host": "https://localhost/x",
		".local host": "https://router.local/x",
		"userinfo":    "https://user:pw@fcm.googleapis.com/x",
		"not a url":   "nonsense",
		"empty":       "",
	} {
		if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(ep, p, a)); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: endpoint %q should be rejected (SSRF guard), got %d", name, ep, r.StatusCode)
		}
	}
	good := "https://updates.push.services.mozilla.com/wpush/v2/abc"
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(good, "short", a)); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad p256dh should be rejected, got %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(good, p, "short")); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad auth should be rejected, got %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", "{not json"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad json should be rejected, got %d", r.StatusCode)
	}
}

func TestPushSubscribeCapsDevices(t *testing.T) {
	srv, ts := setup(t)
	srv.SetPush(func() string { return "PUBKEY" }, nil)
	p, a := pushKeys(t)
	for i := 0; i < maxPushDevices; i++ {
		ep := fmt.Sprintf("https://push.example.com/%d", i)
		if r, b := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody(ep, p, a)); r.StatusCode != http.StatusCreated {
			t.Fatalf("device %d: %d %s", i, r.StatusCode, b)
		}
	}
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody("https://push.example.com/extra", p, a)); r.StatusCode != http.StatusConflict {
		t.Fatalf("over the cap should 409, got %d", r.StatusCode)
	}
	// an already-subscribed device can still refresh its keys at the cap
	if r, _ := postJSON(t, ts.URL+"/api/push/subscribe", "sekrit", subBody("https://push.example.com/3", p, a)); r.StatusCode != http.StatusCreated {
		t.Fatalf("refreshing an existing device at the cap should work, got %d", r.StatusCode)
	}
}

func TestMuteEndpoint(t *testing.T) {
	srv, ts := setup(t)
	if r, _ := get(t, ts.URL+"/api/notifications/mute", "sekrit"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwired mute → 503, got %d", r.StatusCode)
	}
	var raw int64
	srv.SetMute(func() int64 { return raw }, func(v int64) error { raw = v; return nil })

	if r, _ := get(t, ts.URL+"/api/notifications/mute", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mute needs the token, got %d", r.StatusCode)
	}
	view := func(body []byte) (muted bool, until int64) {
		var v struct {
			Muted bool  `json:"muted"`
			Until int64 `json:"until"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("bad json %s: %v", body, err)
		}
		return v.Muted, v.Until
	}
	_, b := get(t, ts.URL+"/api/notifications/mute", "sekrit")
	if m, u := view(b); m || u != 0 {
		t.Fatalf("starts unmuted, got muted=%v until=%d", m, u)
	}

	// 60 minutes → muted until roughly an hour from now
	r, body := postJSON(t, ts.URL+"/api/notifications/mute", "sekrit", `{"minutes":60}`)
	if r.StatusCode != 200 {
		t.Fatalf("mute 60: %d %s", r.StatusCode, body)
	}
	m, u := view([]byte(body))
	hour := time.Now().Add(time.Hour).UnixMilli()
	if !m || u < hour-5000 || u > hour+5000 || raw != u {
		t.Fatalf("muted=%v until=%d raw=%d (want ≈%d)", m, u, raw, hour)
	}
	// indefinitely
	_, body = postJSON(t, ts.URL+"/api/notifications/mute", "sekrit", `{"minutes":-1}`)
	if m, u := view([]byte(body)); !m || u != -1 {
		t.Fatalf("-1 should mute until switched back on: %v %d", m, u)
	}
	// unmute
	_, body = postJSON(t, ts.URL+"/api/notifications/mute", "sekrit", `{"minutes":0}`)
	if m, u := view([]byte(body)); m || u != 0 || raw != 0 {
		t.Fatalf("0 should unmute: %v %d raw=%d", m, u, raw)
	}
	// an expired snooze reads as not muted
	raw = time.Now().Add(-time.Minute).UnixMilli()
	_, b = get(t, ts.URL+"/api/notifications/mute", "sekrit")
	if m, u := view(b); m || u != 0 {
		t.Fatalf("expired snooze must read unmuted: %v %d", m, u)
	}
	for _, bad := range []string{`{}`, `{"minutes":-5}`, `{"minutes":99999999}`, `nope`, `{"minutes":"60"}`} {
		if r, _ := postJSON(t, ts.URL+"/api/notifications/mute", "sekrit", bad); r.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s should be rejected, got %d", bad, r.StatusCode)
		}
	}
}

// A failed test push must NOT be a 5xx: Cloudflare swaps an origin 502/504
// for its own HTML error page, hiding the reason from the user.
func TestPushTestFailureIsNotAServerError(t *testing.T) {
	srv, ts := setup(t)
	srv.SetPush(func() string { return "PUBKEY" }, func(context.Context) (int, error) {
		return 0, fmt.Errorf("could not reach the push service: boom")
	})
	r, body := postJSON(t, ts.URL+"/api/push/test", "sekrit", "{}")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d (a 5xx would be masked by Cloudflare): %s", r.StatusCode, body)
	}
	var v struct {
		Sent  int    `json:"sent"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Sent != 0 || !strings.Contains(v.Error, "boom") {
		t.Fatalf("body = %s (%v)", body, err)
	}
}
