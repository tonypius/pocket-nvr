package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pocketnvr/internal/config"
	"pocketnvr/internal/store"
)

// fakePushStore is an in-memory PushStore.
type fakePushStore struct {
	mu   sync.Mutex
	subs map[string]store.PushSub
	ok   map[string]int
}

func newFakePushStore(subs ...store.PushSub) *fakePushStore {
	f := &fakePushStore{subs: map[string]store.PushSub{}, ok: map[string]int{}}
	for _, s := range subs {
		f.subs[s.Endpoint] = s
	}
	return f
}
func (f *fakePushStore) ListPushSubs() ([]store.PushSub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.PushSub
	for _, s := range f.subs {
		out = append(out, s)
	}
	return out, nil
}
func (f *fakePushStore) DeletePushSub(e string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs, e)
	return nil
}
func (f *fakePushStore) MarkPushOK(e string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[e]++
	return nil
}
func (f *fakePushStore) has(e string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.subs[e]
	return ok
}

// testSub builds a subscription with real P-256 + auth keys, as a browser would.
func testSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return store.PushSub{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func testVAPID(t *testing.T) map[string]string {
	t.Helper()
	m, err := EnsureVAPID(filepath.Join(t.TempDir(), "secrets.yaml"), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newWebPush(ps PushStore, keys map[string]string) *webPush {
	return &webPush{
		store:   ps,
		key:     func(k string) string { return keys[k] },
		subject: func() string { return "mailto:test@example.com" },
		client:  http.DefaultClient,
		logger:  slog.Default(),
	}
}

func TestWebPushDelivers(t *testing.T) {
	var gotAuth, gotEnc, gotTTL, gotUrgency string
	var gotLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEnc = r.Header.Get("Content-Encoding")
		gotTTL = r.Header.Get("TTL")
		gotUrgency = r.Header.Get("Urgency")
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		gotLen = n
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sub := testSub(t, srv.URL+"/push/abc")
	ps := newFakePushStore(sub)
	w := newWebPush(ps, testVAPID(t))
	if err := w.Send(context.Background(), item(7)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "vapid t=") || !strings.Contains(gotAuth, ", k=") {
		t.Fatalf("missing VAPID authorization, got %q", gotAuth)
	}
	if gotEnc != "aes128gcm" {
		t.Fatalf("payload must be RFC 8291 encrypted, Content-Encoding=%q", gotEnc)
	}
	if gotTTL != "3600" || gotUrgency != "high" {
		t.Fatalf("ttl/urgency = %q/%q", gotTTL, gotUrgency)
	}
	if gotLen == 0 {
		t.Fatal("empty push body")
	}
	if ps.ok[sub.Endpoint] != 1 {
		t.Fatal("successful delivery should be recorded")
	}
}

func TestWebPushExpiredSubscriptionRemoved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	sub := testSub(t, srv.URL+"/x")
	ps := newFakePushStore(sub)
	w := newWebPush(ps, testVAPID(t))
	if err := w.Send(context.Background(), item(1)); err != nil {
		t.Fatalf("an expired subscription is not a delivery error: %v", err)
	}
	if ps.has(sub.Endpoint) {
		t.Fatal("410 Gone must remove the subscription")
	}
}

func TestWebPushErrorClassification(t *testing.T) {
	status := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	sub := testSub(t, srv.URL+"/x")
	ps := newFakePushStore(sub)
	w := newWebPush(ps, testVAPID(t))

	// transient (5xx): error so the service retries
	if err := w.Send(context.Background(), item(1)); err == nil {
		t.Fatal("503 should be retriable")
	}
	// rate limited: also retriable
	status = http.StatusTooManyRequests
	if err := w.Send(context.Background(), item(1)); err == nil {
		t.Fatal("429 should be retriable")
	}
	// permanent client error (bad VAPID etc.): must NOT retry forever
	status = http.StatusBadRequest
	if err := w.Send(context.Background(), item(1)); err != nil {
		t.Fatalf("400 must be dropped, not retried: %v", err)
	}
	if !ps.has(sub.Endpoint) {
		t.Fatal("a 400 must not delete the subscription")
	}
}

func TestWebPushMixedDevicesDoNotRetryDelivered(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer good.Close()
	defer bad.Close()
	ps := newFakePushStore(testSub(t, good.URL+"/g"), testSub(t, bad.URL+"/b"))
	w := newWebPush(ps, testVAPID(t))
	// one device got it: succeeding avoids re-notifying it on a retry
	if err := w.Send(context.Background(), item(2)); err != nil {
		t.Fatalf("partial delivery should count as sent: %v", err)
	}
}

func TestWebPushNoSubscribersIsNoop(t *testing.T) {
	w := newWebPush(newFakePushStore(), testVAPID(t))
	if err := w.Send(context.Background(), item(1)); err != nil {
		t.Fatalf("no subscribers must not error: %v", err)
	}
}

func TestWebPushNeedsVAPIDKeys(t *testing.T) {
	w := newWebPush(newFakePushStore(testSub(t, "http://x/y")), map[string]string{})
	if err := w.Send(context.Background(), item(1)); err == nil {
		t.Fatal("missing VAPID keys should error")
	}
}

func TestPayloadFor(t *testing.T) {
	it := item(42)
	p := payloadFor(it)
	if p.URL != "#/events/42" || p.Tag != "event-42" || !strings.Contains(p.Title, "Cam 1") {
		t.Fatalf("payload: %+v", p)
	}
	if p.Image != "" {
		t.Fatal("no base_url → no deep link → no image")
	}
	it.DeepLink = "https://nvr.example.com/api/events/42?token=tok"
	if got := payloadFor(it).Image; got != "https://nvr.example.com/api/events/42/snapshot?token=tok" {
		t.Fatalf("image url = %q", got)
	}
}

func TestEnsureVAPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.yaml")
	os.WriteFile(path, []byte("api_token: abc\n"), 0o600)
	cur, _ := config.LoadSecrets(path)
	a, err := EnsureVAPID(path, cur)
	if err != nil {
		t.Fatal(err)
	}
	if a["vapid_public"] == "" || a["vapid_private"] == "" || a["api_token"] != "abc" {
		t.Fatalf("keys not generated or secrets lost: %v", a)
	}
	// persisted with the existing secrets intact, and private to the owner
	disk, _ := config.LoadSecrets(path)
	if disk["vapid_public"] != a["vapid_public"] || disk["api_token"] != "abc" {
		t.Fatalf("disk secrets: %v", disk)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.yaml perms = %o", st.Mode().Perm())
	}
	// second call must keep the same pair (regenerating would orphan every device)
	b, err := EnsureVAPID(path, disk)
	if err != nil || b["vapid_public"] != a["vapid_public"] || b["vapid_private"] != a["vapid_private"] {
		t.Fatalf("EnsureVAPID must be idempotent: %v %v", err, b)
	}
}

func TestServiceRegistersWebPushAndPushTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer srv.Close()
	keys := testVAPID(t)
	ps := newFakePushStore(testSub(t, srv.URL+"/d"))
	s := New(func() *config.Config { return testCfg() }, func(k string) string { return keys[k] },
		&marker{}, slog.Default())
	if s.hasProviders() {
		t.Fatal("no provider before a push store is wired")
	}
	s.SetPushStore(ps)
	if !s.hasProviders() {
		t.Fatal("web push should register once a store and keys exist")
	}
	n, err := s.PushTest(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("PushTest = %d, %v; want 1 device", n, err)
	}
}

func TestPushTestReportsCauseWithoutLeakingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL + "/secret-device-token"
	srv.Close() // connection refused
	keys := testVAPID(t)
	s := New(func() *config.Config { return testCfg() }, func(k string) string { return keys[k] },
		&marker{}, slog.Default())
	s.SetPushStore(newFakePushStore(testSub(t, endpoint)))
	n, err := s.PushTest(context.Background())
	if err == nil || n != 0 {
		t.Fatalf("want an error for an unreachable push service, got n=%d err=%v", n, err)
	}
	if !strings.Contains(err.Error(), "could not reach the push service") ||
		!strings.Contains(err.Error(), "refused") {
		t.Fatalf("error should state the cause: %v", err)
	}
	if strings.Contains(err.Error(), "secret-device-token") {
		t.Fatalf("the endpoint URL is a per-device capability and must not appear: %v", err)
	}
}
