// Package store is the SQLite event store (FR-EVT-1) with the FRS §5.1
// schema. Uses modernc.org/sqlite so nvrd stays CGO-free and fully static.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Event mirrors the FRS §5.1 `events` table.
type Event struct {
	ID           int64   `json:"id"`
	CameraID     string  `json:"camera_id"`
	Label        string  `json:"label"`
	Score        float64 `json:"score"`
	StartTS      int64   `json:"start_ts"` // epoch ms
	EndTS        *int64  `json:"end_ts"`   // null while active
	Zone         *string `json:"zone"`
	BBox         *string `json:"bbox"` // json [x,y,w,h] of peak frame
	SnapshotPath *string `json:"snapshot_path,omitempty"`
	ClipPath     *string `json:"clip_path,omitempty"`
	CropPath     *string `json:"crop_path,omitempty"`
	CreatedAt    int64   `json:"created_at"`
	Notified     bool    `json:"notified"`
}

// ActiveEvent is the in-progress state written at event start so a crash
// mid-event still leaves a consistent row (FR-EVT-5).
type ActiveEvent struct {
	CameraID  string
	Label     string
	Score     float64
	StartTS   int64
	Zone      *string
	CreatedAt int64
}

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the
// schema. parent dirs must exist.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS cameras (
  id      TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  camera_id     TEXT NOT NULL REFERENCES cameras(id),
  label         TEXT NOT NULL DEFAULT 'person',
  score         REAL NOT NULL,
  start_ts      INTEGER NOT NULL,
  end_ts        INTEGER,
  zone          TEXT,
  bbox          TEXT,
  snapshot_path TEXT,
  clip_path     TEXT,
  crop_path     TEXT,
  created_at    INTEGER NOT NULL,
  notified      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_cam_time ON events(camera_id, start_ts);
CREATE INDEX IF NOT EXISTS idx_events_time ON events(start_ts);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// existing databases: add crop_path if the column is missing (B1)
	s.db.Exec(`ALTER TABLE events ADD COLUMN crop_path TEXT`)
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// SeedCameras upserts the camera roster from config so events always have a
// referenced camera row.
func (s *Store) SeedCameras(cams []struct {
	ID      string
	Name    string
	Enabled bool
}) error {
	for _, c := range cams {
		en := 0
		if c.Enabled {
			en = 1
		}
		if _, err := s.db.Exec(
			`INSERT INTO cameras (id, name, enabled) VALUES (?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET name=excluded.name, enabled=excluded.enabled`,
			c.ID, c.Name, en); err != nil {
			return err
		}
	}
	return nil
}

// StartEvent inserts an active (end_ts NULL) event, crash-safe (FR-EVT-5).
func (s *Store) StartEvent(a ActiveEvent) (int64, error) {
	if a.CreatedAt == 0 {
		a.CreatedAt = time.Now().UnixMilli()
	}
	if a.Label == "" {
		a.Label = "person"
	}
	res, err := s.db.Exec(
		`INSERT INTO events (camera_id, label, score, start_ts, zone, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.CameraID, a.Label, a.Score, a.StartTS, a.Zone, a.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePeak raises score/bbox if the new detection beats the stored peak.
func (s *Store) UpdatePeak(id int64, score float64, bbox *string) error {
	_, err := s.db.Exec(
		`UPDATE events SET score = ?, bbox = COALESCE(?, bbox) WHERE id = ? AND score <= ?`,
		score, bbox, id, score)
	return err
}

// EndEvent closes the event window.
func (s *Store) EndEvent(id int64, endTS int64) error {
	_, err := s.db.Exec(`UPDATE events SET end_ts = ? WHERE id = ? AND end_ts IS NULL`, endTS, id)
	return err
}

// SetAssets attaches snapshot/clip paths (FR-EVT-2, FR-EVT-3).
func (s *Store) SetAssets(id int64, snapshot, clip *string) error {
	_, err := s.db.Exec(`UPDATE events SET snapshot_path = COALESCE(?, snapshot_path), clip_path = COALESCE(?, clip_path) WHERE id = ?`, snapshot, clip, id)
	return err
}

// SetCrop attaches the cropped-thumbnail path (B1).
func (s *Store) SetCrop(id int64, path string) error {
	_, err := s.db.Exec(`UPDATE events SET crop_path = ? WHERE id = ?`, path, id)
	return err
}

// MarkNotified records notification success for the retry queue (FR-NOT-4).
func (s *Store) MarkNotified(id int64) error {
	_, err := s.db.Exec(`UPDATE events SET notified = 1 WHERE id = ?`, id)
	return err
}

// Unnotified returns events with notified=0 older than a grace period.
func (s *Store) Unnotified(limit int) ([]Event, error) {
	return s.list(`WHERE notified = 0 AND end_ts IS NOT NULL ORDER BY start_ts LIMIT ` + itoa(int64(limit)))
}

// Event fetches one event.
func (s *Store) Event(id int64) (*Event, error) {
	rows, err := s.list(`WHERE id = ` + itoa(id))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ListEvents queries by camera / time window / label with a limit
// (FR-API-1: /api/events).
func (s *Store) ListEvents(camera string, fromMS, toMS int64, label string, limit int) ([]Event, error) {
	var cond []string
	var args []any
	if camera != "" {
		cond = append(cond, "camera_id = ?")
		args = append(args, camera)
	}
	if fromMS > 0 {
		cond = append(cond, "start_ts >= ?")
		args = append(args, fromMS)
	}
	if toMS > 0 {
		cond = append(cond, "start_ts <= ?")
		args = append(args, toMS)
	}
	if label != "" {
		cond = append(cond, "label = ?")
		args = append(args, label)
	}
	q := ""
	if len(cond) > 0 {
		q = "WHERE " + joinAnd(cond)
	}
	q += " ORDER BY start_ts DESC"
	if limit > 0 {
		q += " LIMIT " + itoa(int64(limit))
	} else {
		q += " LIMIT 500"
	}
	rows, err := s.list(q, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

const eventCols = `id, camera_id, label, score, start_ts, end_ts, zone, bbox, snapshot_path, clip_path, crop_path, created_at, notified`

func (s *Store) list(where string, args ...any) ([]Event, error) {
	rows, err := s.db.Query("SELECT "+eventCols+" FROM events "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var notified int
		if err := rows.Scan(&e.ID, &e.CameraID, &e.Label, &e.Score, &e.StartTS, &e.EndTS,
			&e.Zone, &e.BBox, &e.SnapshotPath, &e.ClipPath, &e.CropPath, &e.CreatedAt, &notified); err != nil {
			return nil, err
		}
		e.Notified = notified == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneOlder deletes events (and reports their asset paths so the caller can
// remove files — DB and files die together, FR-EVT-4).
func (s *Store) PruneOlder(beforeMS int64) (paths []string, err error) {
	rows, err := s.db.Query(
		`SELECT snapshot_path, clip_path, crop_path FROM events WHERE start_ts < ? AND (snapshot_path IS NOT NULL OR clip_path IS NOT NULL OR crop_path IS NOT NULL)`,
		beforeMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var snap, clip, crop sql.NullString
		if err := rows.Scan(&snap, &clip, &crop); err != nil {
			return nil, err
		}
		if snap.Valid {
			paths = append(paths, snap.String)
		}
		if clip.Valid {
			paths = append(paths, clip.String)
		}
		if crop.Valid {
			paths = append(paths, crop.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`DELETE FROM events WHERE start_ts < ?`, beforeMS)
	return paths, err
}

// CountEvents returns the total event count (metrics, FR-OBS-2).
func (s *Store) CountEvents() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

// PruneOldestN removes the n oldest events and reports their asset paths
// (FR-EVT-4 count-based retention; DB and files die together).
func (s *Store) PruneOldestN(n int64) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, snapshot_path, clip_path, crop_path FROM events ORDER BY start_ts ASC LIMIT ` + itoa(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var snap, clip, crop sql.NullString
		if err := rows.Scan(&id, &snap, &clip, &crop); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if snap.Valid && snap.String != "" {
			paths = append(paths, snap.String)
		}
		if clip.Valid && clip.String != "" {
			paths = append(paths, clip.String)
		}
		if crop.Valid && crop.String != "" {
			paths = append(paths, crop.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM events WHERE id = ?`, id); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

// ReferencedAssets returns every snapshot/clip/crop path still referenced
// by a live event row (input for the retention orphan sweep).
func (s *Store) ReferencedAssets() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT snapshot_path, clip_path, crop_path FROM events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]struct{})
	for rows.Next() {
		var snap, clip, crop sql.NullString
		if err := rows.Scan(&snap, &clip, &crop); err != nil {
			return nil, err
		}
		for _, v := range []sql.NullString{snap, clip, crop} {
			if v.Valid && v.String != "" {
				set[v.String] = struct{}{}
			}
		}
	}
	return set, rows.Err()
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
