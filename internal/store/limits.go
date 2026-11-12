package store

import (
	"fmt"
	"time"
)

// UsageReading is one stored usage snapshot: the 5-hour session and weekly
// utilization of the plan's limits, in percent, with their reset times.
type UsageReading struct {
	Time           time.Time
	SessionPct     float64
	WeeklyPct      float64
	SessionResetAt string
	WeeklyResetAt  string
}

// UsageReadings returns the usage snapshots taken since since, oldest
// first.
func (s *Store) UsageReadings(since time.Time) ([]UsageReading, error) {
	rows, err := s.db.Query(`
		SELECT timestamp, session_pct, weekly_pct, COALESCE(session_reset_at, ''), COALESCE(weekly_reset_at, '')
		FROM usage_snapshots WHERE timestamp >= ? ORDER BY timestamp ASC`,
		since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("querying usage readings: %w", err)
	}
	defer rows.Close()
	var out []UsageReading
	for rows.Next() {
		var r UsageReading
		var ts string
		if err := rows.Scan(&ts, &r.SessionPct, &r.WeeklyPct, &r.SessionResetAt, &r.WeeklyResetAt); err != nil {
			return nil, fmt.Errorf("scanning usage reading: %w", err)
		}
		t, err := parseSnapshotTime(ts)
		if err != nil {
			continue
		}
		r.Time = t
		out = append(out, r)
	}
	return out, rows.Err()
}
