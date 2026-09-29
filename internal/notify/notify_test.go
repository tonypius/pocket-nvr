package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"pocketnvr/internal/config"
)

// marker records MarkNotified calls.
type marker struct {
	mu    sync.Mutex
	marks []int64
}

func (m *marker) MarkNotified(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks = append(m.marks, id)
	return nil
}

func (m *marker) marked(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.marks {
		if v == id {
			return true
		}
	}
	return false
}

func testCfg() *config.Config {
	return &config.Config{Notifications: config.Notifications{
		Provider: "ntfy", CooldownSeconds: 60, GlobalMinGapSeconds: 30,
	}}
}

func newService(t *testing.T, cfg *config.Config, url string) (*Service, *marker) {
	t.Helper()
	st := &marker{}
	var providers = []Notifier{&ntfy{url: url, client: http.DefaultClient}}
	s := &Service{
		lastPerCa: map[string]time.Time{},
		cfg:       func() *config.Config { return cfg },
		token:     func(string) string { return "" },
		providers: providers,
		store:     st,
		logger:    slog.Default(),
		client:    http.DefaultClient,
		q:         make(chan Item, 16),
		nowFn:     func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) },
	}
	return s, st
}

func item(id int64) Item {
	return Item{EventID: id, CameraID: "cam1", Camera: "Cam 1",
		Score: 0.9, StartTS: time.Now().UnixMilli(), Snapshot: []byte("jpegbytes")}
}

func TestDeliveryAndCooldown(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.Method != http.MethodPut {
			t.Errorf("ntfy attach wants PUT, got %s", r.Method)
		}
		if r.Header.Get("Filename") == "" {
			t.Error("attachment without Filename header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "jpegbytes" {
			t.Errorf("snapshot not sent as body: %q", body)
		}
	}))
	defer ts.Close()

	s, st := newService(t, testCfg(), ts.URL)
	ok, _ := s.Enqueue(item(1))
	if !ok {
		t.Fatal("first event should queue")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	waitFor(t, func() bool { return st.marked(1) }, 2*time.Second)
	cancel()

	// second event same camera within cooldown → suppressed + marked
	ok, why := s.Enqueue(item(2))
	if ok || !strings.Contains(why, "cooldown") {
		t.Fatalf("want cooldown suppression, got ok=%v why=%q", ok, why)
	}
	mu.Lock()
	c := calls
	mu.Unlock()
	if c != 1 {
		t.Fatalf("provider called %d times, want 1", c)
	}
	if !st.marked(2) {
		t.Error("suppressed event should be marked to avoid replay")
	}
}

func TestGlobalMinGap(t *testing.T) {
	s, _ := newService(t, testCfg(), "http://unused")
	if ok, why := s.Enqueue(item(1)); !ok || why != "" {
		t.Fatalf("first should pass: %v %q", ok, why)
	}
	it := item(2)
	it.CameraID = "cam2" // different camera → global gap applies
	if ok, why := s.Enqueue(it); ok || !strings.Contains(why, "min-gap") {
		t.Fatalf("want global min-gap suppression, got ok=%v why=%q", ok, why)
	}
}

func TestQuietHours(t *testing.T) {
	cfg := testCfg()
	cfg.Notifications.QuietStart = "23:00"
	cfg.Notifications.QuietEnd = "06:30"
	s, st := newService(t, cfg, "http://unused")
	// nowFn is 12:00 UTC → not quiet
	if ok, _ := s.Enqueue(item(1)); !ok {
		t.Fatal("midday should not be quiet")
	}
	// move clock into the window (23:30)
	s.nowFn = func() time.Time { return time.Date(2026, 9, 8, 23, 30, 0, 0, time.UTC) }
	s.lastPerCa = map[string]time.Time{}
	s.lastAny = time.Time{}
	ok, why := s.Enqueue(item(3))
	if ok || !strings.Contains(why, "quiet") {
		t.Fatalf("want quiet-hour suppression, got ok=%v why=%q", ok, why)
	}
	if !st.marked(3) {
		t.Error("quiet-suppressed event should be marked")
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var mu sync.Mutex
	fail := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()

	cfg := testCfg()
	cfg.Notifications.GlobalMinGapSeconds = 0 // don't gate retries in test
	cfg.Notifications.CooldownSeconds = 0
	s, st := newService(t, cfg, ts.URL)
	s.Enqueue(item(7))

	// succeed on the 2nd attempt
	go func() {
		time.Sleep(1100 * time.Millisecond) // after first 1s backoff
		mu.Lock()
		fail = false
		mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	waitFor(t, func() bool { return st.marked(7) }, 10*time.Second)
	cancel()
}

func TestInQuietHoursFn(t *testing.T) {
	if !inQuietHours(time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC), 23*60, 6*60+30) {
		t.Error("23:30 in 23:00-06:30")
	}
	if !inQuietHours(time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC), 23*60, 6*60+30) {
		t.Error("03:00 in 23:00-06:30 (crosses midnight)")
	}
	if inQuietHours(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), 23*60, 6*60+30) {
		t.Error("12:00 not quiet")
	}
}

func waitFor(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

var _ = json.Marshal // keep import if assertions change
