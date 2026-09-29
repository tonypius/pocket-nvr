// Package retention enforces FR-EVT-4 (independent event/snapshot/clip
// retention by age and count, DB rows and files die together) and the
// recording side of FR-MED-3 / FR-SUP-8 (age + size cap, pruned oldest
// first). Runs as a loop inside nvrd.
package retention

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"pocketnvr/internal/config"
	"pocketnvr/internal/store"
)

type Store interface {
	PruneOlder(beforeMS int64) ([]string, error)
	CountEvents() (int64, error)
	PruneOldestN(n int64) ([]string, error)
}

// Run performs one pass every interval until ctx is done.
func Run(ctx context.Context, st *store.Store, cfg func() *config.Config, logger *slog.Logger, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Pass(st, cfg(), logger)
		}
	}
}

// Pass executes one retention sweep.
func Pass(st *store.Store, c *config.Config, logger *slog.Logger) {
	// Events by age (FR-EVT-4).
	cutoff := time.Now().Add(-time.Duration(c.Events.RetentionDays) * 24 * time.Hour).UnixMilli()
	paths, err := st.PruneOlder(cutoff)
	if err != nil {
		logger.Error("event age prune", "err", err)
	}
	removeFiles(paths, logger)

	// Events by count.
	if c.Events.RetentionMaxCount > 0 {
		if n, err := st.CountEvents(); err == nil && n > int64(c.Events.RetentionMaxCount) {
			paths, err := st.PruneOldestN(n - int64(c.Events.RetentionMaxCount))
			if err != nil {
				logger.Error("event count prune", "err", err)
			}
			removeFiles(paths, logger)
		}
	}

	// Recordings by age, then size cap (FR-MED-3).
	recordings := filepath.Join(c.System.BasePath, "recordings")
	byAge := pruneRecordingAge(recordings, c.Recording.RetentionDays)
	if len(byAge) > 0 {
		logger.Info("pruned recordings (age)", "files", len(byAge))
	}
	if freed := pruneRecordingSize(recordings, c.SizeCapBytes()); freed > 0 {
		logger.Info("pruned recordings (size cap)", "freed_bytes", freed)
	}

	// Orphan sweep: asset files whose DB row is already gone (a remove that
	// failed once, a crash between row delete and file delete) have nothing
	// left to revisit them — without this they leak forever, and the
	// free-space guard has no row to prune to reclaim them.
	ref, err := st.ReferencedAssets()
	if err != nil {
		logger.Error("orphan sweep: list referenced assets", "err", err)
		return
	}
	removed := sweepOrphans(filepath.Join(c.System.BasePath, "snapshots"), ref, logger)
	removed += sweepOrphans(filepath.Join(c.System.BasePath, "clips"), ref, logger)
	if removed > 0 {
		logger.Info("pruned orphaned assets", "files", removed)
	}
}

// orphanGrace keeps the sweep away from files still being written for a
// just-inserted event (row insert, file write, and SetAssets aren't atomic).
const orphanGrace = time.Hour

// sweepOrphans deletes unreferenced files in a flat asset dir. Returns the
// number removed.
func sweepOrphans(dir string, referenced map[string]struct{}, logger *slog.Logger) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // missing dir is fine
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, ok := referenced[p]; ok {
			continue
		}
		if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < orphanGrace {
			continue
		}
		if err := os.Remove(p); err != nil {
			logger.Warn("remove orphan", "path", p, "err", err)
			continue
		}
		removed++
	}
	return removed
}

func removeFiles(paths []string, logger *slog.Logger) {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove file", "path", p, "err", err)
		}
	}
}

// pruneRecordingAge deletes recording files older than days. Returns paths.
func pruneRecordingAge(dir string, days int) []string {
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var removed []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // keep walking (missing dir is fine)
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if rmErr := os.Remove(path); rmErr == nil {
				removed = append(removed, path)
			}
		}
		return nil
	})
	return removed
}

// pruneRecordingSize deletes oldest files first until total size <= cap.
// Returns bytes freed.
func pruneRecordingSize(dir string, capBytes int64) int64 {
	if capBytes <= 0 {
		return 0
	}
	type file struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []file
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, file{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= capBytes {
		return 0
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	var freed int64
	for _, f := range files {
		if total <= capBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			freed += f.size
		}
	}
	return freed
}
