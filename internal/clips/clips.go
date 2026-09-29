// Package clips extracts event clips (FR-EVT-3): the recorded main-stream
// segments covering [start − pre, end + post] are concatenated with
// stream-copy — no re-encode — into <base>/clips/<event>.mp4.
package clips

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocketnvr/internal/config"
)

type AssetSetter interface {
	SetAssets(id int64, snapshot, clip *string) error
}

type Job struct {
	EventID  int64
	CameraID string
	StartTS  int64 // epoch ms
	EndTS    int64 // epoch ms
}

type Extractor struct {
	cfg    func() *config.Config
	marker AssetSetter
	logger *slog.Logger
	ffmpeg string
	queue  chan Job
	once   sync.Once
}

func New(cfg func() *config.Config, marker AssetSetter, logger *slog.Logger, ffmpegPath string) *Extractor {
	return &Extractor{cfg: cfg, marker: marker, logger: logger, ffmpeg: ffmpegPath, queue: make(chan Job, 64)}
}

// Enqueue schedules a clip extraction; never blocks the caller.
func (x *Extractor) Enqueue(j Job) {
	select {
	case x.queue <- j:
	default:
		x.logger.Warn("clip queue full, dropping extraction", "event", j.EventID)
	}
}

// Run processes extractions sequentially (IO-heavy) until stop closes.
func (x *Extractor) Run(stop <-chan struct{}) {
	x.once.Do(func() {
		for {
			select {
			case <-stop:
				return
			case j := <-x.queue:
				if err := x.extract(j); err != nil {
					x.logger.Warn("clip extraction failed", "event", j.EventID, "err", err)
				}
			}
		}
	})
}

func (x *Extractor) extract(j Job) error {
	c := x.cfg()
	dir := filepath.Join(c.System.BasePath, "recordings", j.CameraID)
	windowStart := time.UnixMilli(j.StartTS).Add(-time.Duration(c.Events.ClipPreSeconds) * time.Second)
	windowEnd := time.UnixMilli(j.EndTS).Add(time.Duration(c.Events.ClipPostSeconds) * time.Second)
	// fmp4 segments are capped by recordSegmentDuration; include any file
	// that could straddle the padded window edges.
	slack := time.Duration(c.Recording.SegmentSeconds)*time.Second + 2*time.Second

	segments, err := segmentsOverlapping(dir, windowStart.Add(-slack), windowEnd.Add(slack))
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("no recordings cover %s..%s",
			windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))
	}

	clipPath := filepath.Join(c.System.BasePath, "clips", fmt.Sprintf("%d.mp4", j.EventID))
	if err := os.MkdirAll(filepath.Dir(clipPath), 0o755); err != nil {
		return err
	}

	list := filepath.Join(c.System.BasePath, "clips", fmt.Sprintf("%d.txt", j.EventID))
	var sb strings.Builder
	for _, s := range segments {
		// concat demuxer resolves relative paths against the LIST file's
		// directory, not the CWD — always write absolute paths.
		abs, err := filepath.Abs(s)
		if err != nil {
			return err
		}
		sb.WriteString("file '" + abs + "'\n")
	}
	if err := os.WriteFile(list, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	defer os.Remove(list)

	// stream-copy concat: segments are already H264-in-MP4 (FR-EVT-3)
	cmd := exec.Command(x.ffmpeg, "-y", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list,
		"-c", "copy", "-movflags", "+faststart", clipPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat: %v: %s", err, string(out))
	}
	st, err := os.Stat(clipPath)
	if err != nil || st.Size() == 0 {
		return fmt.Errorf("clip missing or empty")
	}
	return x.marker.SetAssets(j.EventID, nil, &clipPath)
}

// segmentsOverlapping returns recording files whose parsed segment-start
// timestamp falls inside [from, to], sorted ascending.
func segmentsOverlapping(dir string, from, to time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("recordings dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
			continue
		}
		ts, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		if !ts.Before(from) && !ts.After(to) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// parseSegmentName parses the MediaMTX recordPath tail
// %Y-%m-%d_%H-%M-%S-%f, e.g. 2026-09-08_18-14-41-123456.mp4. Note the
// underscore between date and time; %f is microseconds (we only use it to
// keep segments ordered — clamped to seconds is fine at segment
// granularity).
func parseSegmentName(name string) (time.Time, bool) {
	base := filepath.Base(name)
	dot := strings.LastIndex(base, ".")
	if dot < 0 {
		return time.Time{}, false
	}
	stamp := strings.ReplaceAll(base[:dot], "_", "-")
	parts := strings.Split(stamp, "-")
	if len(parts) < 6 {
		return time.Time{}, false
	}
	var nums [6]int
	for i := 0; i < 6; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil {
			return time.Time{}, false
		}
		nums[i] = v
	}
	if nums[1] < 1 || nums[1] > 12 || nums[2] < 1 || nums[2] > 31 ||
		nums[3] > 23 || nums[4] > 59 || nums[5] > 59 {
		return time.Time{}, false
	}
	return time.Date(nums[0], time.Month(nums[1]), nums[2], nums[3], nums[4], nums[5], 0, time.Local), true
}
