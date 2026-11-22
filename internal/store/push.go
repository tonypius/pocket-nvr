package store

import "time"

// PushSub is one browser/PWA Web Push subscription (RFC 8030). The endpoint
// is the push service URL for one device; p256dh/auth are that device's
// public encryption keys (RFC 8291) — not secrets of ours.
type PushSub struct {
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"p256dh"`
	Auth      string `json:"auth"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastOKAt  *int64 `json:"last_ok_at"`
}

// UpsertPushSub stores a subscription, refreshing keys if the same endpoint
// subscribes again (browsers re-subscribe with new keys after resets).
func (s *Store) UpsertPushSub(p PushSub) error {
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO push_subscriptions (endpoint, p256dh, auth, user_agent, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET
		   p256dh=excluded.p256dh, auth=excluded.auth, user_agent=excluded.user_agent`,
		p.Endpoint, p.P256dh, p.Auth, p.UserAgent, p.CreatedAt)
	return err
}

// DeletePushSub removes a subscription (user opted out, or the push service
// reported it gone). Deleting an unknown endpoint is not an error.
func (s *Store) DeletePushSub(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// ListPushSubs returns every subscription, oldest first.
func (s *Store) ListPushSubs() ([]PushSub, error) {
	rows, err := s.db.Query(
		`SELECT endpoint, p256dh, auth, user_agent, created_at, last_ok_at
		 FROM push_subscriptions ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		var last *int64
		if err := rows.Scan(&p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt, &last); err != nil {
			return nil, err
		}
		p.LastOKAt = last
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPushSubs is used to cap how many devices may subscribe.
func (s *Store) CountPushSubs() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions`).Scan(&n)
	return n, err
}

// MarkPushOK records a successful delivery to an endpoint.
func (s *Store) MarkPushOK(endpoint string) error {
	_, err := s.db.Exec(`UPDATE push_subscriptions SET last_ok_at = ? WHERE endpoint = ?`,
		time.Now().UnixMilli(), endpoint)
	return err
}
