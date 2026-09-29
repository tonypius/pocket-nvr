package clips

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSegmentName(t *testing.T) {
	ts, ok := parseSegmentName("2026-09-08_18-36-36-495018.mp4")
	if !ok {
		t.Fatal("valid name rejected")
	}
	if ts.Month() != time.September || ts.Day() != 8 || ts.Hour() != 18 ||
		ts.Minute() != 36 || ts.Second() != 36 {
		t.Fatalf("wrong time parsed: %v", ts)
	}
	if _, ok := parseSegmentName("junk.mp4"); ok {
		t.Error("junk name accepted")
	}
	if _, ok := parseSegmentName("2026-13-08_18-36-36-1.mp4"); ok {
		t.Error("month 13 accepted")
	}
}

func TestSegmentsOverlapping(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"2026-09-08_10-00-00-000000.mp4",
		"2026-09-08_10-00-02-000000.mp4",
		"2026-09-08_10-00-04-000000.mp4",
		"2026-09-08_10-00-06-000000.mp4",
		"ignore.txt",
	}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	from := time.Date(2026, 9, 8, 10, 0, 1, 0, time.Local)
	to := time.Date(2026, 9, 8, 10, 0, 5, 0, time.Local)
	got, err := segmentsOverlapping(dir, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// 10-00-02 and 10-00-04 fall inside; 10-00-00 is before, 10-00-06 after
	if len(got) != 2 {
		t.Fatalf("want 2 segments, got %d: %v", len(got), got)
	}
	if filepath.Base(got[0]) != "2026-09-08_10-00-02-000000.mp4" {
		t.Fatalf("not sorted / wrong first: %v", got[0])
	}
}
