package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Multi-machine mode keeps, next to daily_aggregates, the same totals split
// by machine, so the history can be shown per machine after the transcripts
// are gone. Single-machine mode leaves this table empty.

// MachineDay identifies one machine's row of one day.
type MachineDay struct {
	Date    string
	Machine string
}

func migrateMachines(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_machine_aggregates (
			date TEXT NOT NULL,
			machine TEXT NOT NULL,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_create_tokens INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0,
			session_count INTEGER DEFAULT 0,
			message_count INTEGER DEFAULT 0,
			PRIMARY KEY (date, machine)
		);
		CREATE INDEX IF NOT EXISTS idx_machine_aggregates_machine ON daily_machine_aggregates(machine, date);
	`)
	return err
}

// UpsertMachineDailyAggregate writes one machine's totals for agg.Date.
func (s *Store) UpsertMachineDailyAggregate(machine string, agg DailyAggregate) error {
	_, err := s.db.Exec(`
		INSERT INTO daily_machine_aggregates (date, machine, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, cost_usd, session_count, message_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, machine) DO UPDATE SET
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_create_tokens = excluded.cache_create_tokens,
			cost_usd = excluded.cost_usd,
			session_count = excluded.session_count,
			message_count = excluded.message_count
	`, agg.Date, machine, agg.InputTokens, agg.OutputTokens, agg.CacheReadTokens, agg.CacheCreateTokens, agg.CostUSD, agg.SessionCount, agg.MessageCount)
	if err != nil {
		return fmt.Errorf("upserting machine aggregate for %s/%s: %w", agg.Date, machine, err)
	}
	return nil
}

// MachineDailyMessageCounts returns the stored message count of every
// machine row.
func (s *Store) MachineDailyMessageCounts() (map[MachineDay]int, error) {
	rows, err := s.db.Query("SELECT date, machine, message_count FROM daily_machine_aggregates")
	if err != nil {
		return nil, fmt.Errorf("querying machine message counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[MachineDay]int)
	for rows.Next() {
		var k MachineDay
		var n int
		if err := rows.Scan(&k.Date, &k.Machine, &n); err != nil {
			return nil, fmt.Errorf("scanning machine message count: %w", err)
		}
		counts[k] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating machine message counts: %w", err)
	}
	return counts, nil
}

// Machines returns the names of the machines with stored history, sorted.
func (s *Store) Machines() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT machine FROM daily_machine_aggregates ORDER BY machine")
	if err != nil {
		return nil, fmt.Errorf("querying machines: %w", err)
	}
	defer rows.Close()

	machines := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scanning machine: %w", err)
		}
		machines = append(machines, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating machines: %w", err)
	}
	return machines, nil
}

// GetMachineHistory is GetHistory for one machine: a continuous zero-filled
// series of `days` local days ending today.
func (s *Store) GetMachineHistory(machine string, days int) ([]DailyAggregate, error) {
	now := time.Now()
	since := now.AddDate(0, 0, -(days - 1))
	rows, err := s.db.Query(
		`SELECT date, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, cost_usd, session_count, message_count
		 FROM daily_machine_aggregates WHERE machine = ? AND date >= ? ORDER BY date ASC`, machine, since.Format("2006-01-02"),
	)
	if err != nil {
		return nil, fmt.Errorf("querying machine history: %w", err)
	}
	defer rows.Close()

	byDate := make(map[string]DailyAggregate)
	for rows.Next() {
		var a DailyAggregate
		if err := rows.Scan(&a.Date, &a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheCreateTokens, &a.CostUSD, &a.SessionCount, &a.MessageCount); err != nil {
			return nil, fmt.Errorf("scanning machine history row: %w", err)
		}
		byDate[a.Date] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating machine history rows: %w", err)
	}

	result := make([]DailyAggregate, 0, days)
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		if a, ok := byDate[date]; ok {
			result = append(result, a)
		} else {
			result = append(result, DailyAggregate{Date: date})
		}
	}
	return result, nil
}

// pruneMachines deletes the machine rows older than cutoff (a date).
func (s *Store) pruneMachines(cutoff string) (int64, error) {
	res, err := s.db.Exec("DELETE FROM daily_machine_aggregates WHERE date < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning machine aggregates: %w", err)
	}
	return res.RowsAffected()
}

// MachineStatus is when a machine's agent last contacted the server, and
// when it last sent new session data (multi-machine mode).
type MachineStatus struct {
	LastSeen time.Time
	LastData time.Time // zero when no data was received
}

func migrateMachineStatus(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS machine_status (
			machine TEXT PRIMARY KEY,
			last_seen INTEGER NOT NULL DEFAULT 0,
			last_data INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}

// TouchMachine records that a machine's agent contacted the server at at,
// and sent new session data when gotData is set. Times only move forward.
func (s *Store) TouchMachine(machine string, at time.Time, gotData bool) error {
	data := int64(0)
	if gotData {
		data = at.UnixNano()
	}
	_, err := s.db.Exec(`
		INSERT INTO machine_status (machine, last_seen, last_data) VALUES (?, ?, ?)
		ON CONFLICT(machine) DO UPDATE SET
			last_seen = MAX(last_seen, excluded.last_seen),
			last_data = MAX(last_data, excluded.last_data)
	`, machine, at.UnixNano(), data)
	if err != nil {
		return fmt.Errorf("recording status of machine %s: %w", machine, err)
	}
	return nil
}

// MachineStatuses returns the recorded status of every machine, keyed by
// the name its agent sends.
func (s *Store) MachineStatuses() (map[string]MachineStatus, error) {
	rows, err := s.db.Query("SELECT machine, last_seen, last_data FROM machine_status")
	if err != nil {
		return nil, fmt.Errorf("querying machine status: %w", err)
	}
	defer rows.Close()
	out := make(map[string]MachineStatus)
	for rows.Next() {
		var machine string
		var seen, data int64
		if err := rows.Scan(&machine, &seen, &data); err != nil {
			return nil, fmt.Errorf("scanning machine status: %w", err)
		}
		st := MachineStatus{LastSeen: time.Unix(0, seen)}
		if data > 0 {
			st.LastData = time.Unix(0, data)
		}
		out[machine] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating machine status: %w", err)
	}
	return out, nil
}
