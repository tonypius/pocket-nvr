package store

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SeedCameras([]struct {
		ID      string
		Name    string
		Enabled bool
	}{{"front_tapo", "Front Door", true}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEventLifecycle(t *testing.T) {
	s := open(t)
	start := time.Now().UnixMilli()

	id, err := s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.7, StartTS: start, CreatedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePeak(id, 0.91, strPtr(`[412,220,96,210]`)); err != nil {
		t.Fatal(err)
	}
	// lower score must not clobber peak
	if err := s.UpdatePeak(id, 0.5, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.EndEvent(id, start+6000); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAssets(id, strPtr("/data/nvr/snapshots/1.jpg"), strPtr("/data/nvr/clips/1.mp4")); err != nil {
		t.Fatal(err)
	}

	e, err := s.Event(id)
	if err != nil || e == nil {
		t.Fatalf("Event(%d): %v", id, err)
	}
	if e.Score != 0.91 || e.BBox == nil || e.EndTS == nil || e.SnapshotPath == nil {
		t.Fatalf("bad event: %+v", e)
	}

	got, err := s.ListEvents("front_tapo", 0, 0, "person", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListEvents: %v %d", err, len(got))
	}
	if got[0].ID != id {
		t.Errorf("wrong event returned")
	}
}

func TestUnnotifiedAndMark(t *testing.T) {
	s := open(t)
	now := time.Now().UnixMilli()
	id, _ := s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.9, StartTS: now, CreatedAt: now})
	s.EndEvent(id, now+1000)

	pending, err := s.Unnotified(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("Unnotified: %v %d", err, len(pending))
	}
	if err := s.MarkNotified(id); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.Unnotified(10)
	if len(pending) != 0 {
		t.Fatalf("MarkNotified did not stick: %d", len(pending))
	}
}

func TestPrune(t *testing.T) {
	s := open(t)
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	id, _ := s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.9, StartTS: old, CreatedAt: old})
	s.SetAssets(id, strPtr("/data/nvr/snapshots/old.jpg"), nil)
	recent := time.Now().UnixMilli()
	s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.9, StartTS: recent, CreatedAt: recent})

	paths, err := s.PruneOlder(time.Now().Add(-24 * time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/data/nvr/snapshots/old.jpg" {
		t.Fatalf("prune paths: %v", paths)
	}
	got, _ := s.ListEvents("", 0, 0, "", 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 event after prune, got %d", len(got))
	}
}

func TestPruneReportsCropAsset(t *testing.T) {
	s := open(t)
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	id, _ := s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.9, StartTS: old, CreatedAt: old})
	if err := s.SetAssets(id, strPtr("/s/1.jpg"), strPtr("/c/1.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCrop(id, "/s/1-crop.jpg"); err != nil {
		t.Fatal(err)
	}

	paths, err := s.PruneOlder(time.Now().Add(-24 * time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	for _, want := range []string{"/s/1.jpg", "/c/1.mp4", "/s/1-crop.jpg"} {
		if !got[want] {
			t.Errorf("PruneOlder missing asset %s: %v", want, paths)
		}
	}

	// count-based prune must report crops too
	id2, _ := s.StartEvent(ActiveEvent{CameraID: "front_tapo", Score: 0.9, StartTS: old + 1, CreatedAt: old})
	s.SetAssets(id2, strPtr("/s/2.jpg"), nil)
	s.SetCrop(id2, "/s/2-crop.jpg")
	paths, err = s.PruneOldestN(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/s/2.jpg" || paths[1] != "/s/2-crop.jpg" {
		t.Errorf("PruneOldestN paths: %v", paths)
	}

	ref, err := s.ReferencedAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(ref) != 0 {
		t.Errorf("ReferencedAssets after pruning everything: %v", ref)
	}
}

func strPtr(s string) *string { return &s }
