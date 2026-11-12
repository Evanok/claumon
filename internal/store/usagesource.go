package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageSource describes the newest usage reading pushed by an agent
// (multi-machine mode): when it was fetched, by which machine, and the
// subscription of that machine's Claude credentials. A server without
// credentials of its own reports this subscription.
type UsageSource struct {
	FetchedAt    time.Time
	Machine      string
	Subscription string
	Tier         string
}

func migrateUsageSource(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_source (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			fetched_at INTEGER NOT NULL,
			machine TEXT NOT NULL,
			subscription TEXT NOT NULL DEFAULT '',
			tier TEXT NOT NULL DEFAULT ''
		);
	`)
	return err
}

// SaveUsageSource records the source of the newest pushed usage reading.
func (s *Store) SaveUsageSource(u UsageSource) error {
	_, err := s.db.Exec(`
		INSERT INTO usage_source (id, fetched_at, machine, subscription, tier) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET fetched_at = excluded.fetched_at, machine = excluded.machine,
			subscription = excluded.subscription, tier = excluded.tier
	`, u.FetchedAt.UnixNano(), u.Machine, u.Subscription, u.Tier)
	if err != nil {
		return fmt.Errorf("saving usage source: %w", err)
	}
	return nil
}

// LastUsageSource returns the source of the newest pushed usage reading.
func (s *Store) LastUsageSource() (UsageSource, bool, error) {
	var u UsageSource
	var at int64
	err := s.db.QueryRow(`SELECT fetched_at, machine, subscription, tier FROM usage_source WHERE id = 1`).
		Scan(&at, &u.Machine, &u.Subscription, &u.Tier)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageSource{}, false, nil
	}
	if err != nil {
		return UsageSource{}, false, fmt.Errorf("reading usage source: %w", err)
	}
	u.FetchedAt = time.Unix(0, at)
	return u, true, nil
}

// LatestUsageSnapshot returns the raw usage API response of the newest
// snapshot and when it was saved.
func (s *Store) LatestUsageSnapshot() (raw []byte, at time.Time, ok bool, err error) {
	var ts string
	err = s.db.QueryRow(`SELECT timestamp, raw_json FROM usage_snapshots ORDER BY timestamp DESC LIMIT 1`).Scan(&ts, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("reading latest snapshot: %w", err)
	}
	at, err = parseSnapshotTime(ts)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("parsing latest snapshot time %q: %w", ts, err)
	}
	return raw, at, true, nil
}
