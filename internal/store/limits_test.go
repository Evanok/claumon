package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUsageReadings(t *testing.T) {
	st := openTestStore(t)
	insert := func(at time.Time, session, weekly float64, sReset, wReset string) {
		t.Helper()
		_, err := st.db.Exec(`INSERT INTO usage_snapshots (timestamp, session_pct, weekly_pct, session_reset_at, weekly_reset_at, raw_json) VALUES (?, ?, ?, ?, ?, ?)`,
			at.UTC().Format("2006-01-02 15:04:05"), session, weekly, sReset, wReset, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	insert(now.Add(-10*24*time.Hour), 50, 90, "s0", "w0") // before the window
	insert(now.Add(-2*time.Hour), 10, 20, "s1", "w1")
	insert(now.Add(-time.Hour), 30, 25, "s1", "w1")

	got, err := st.UsageReadings(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("readings = %+v, want the 2 within the window", got)
	}
	if got[0].SessionPct != 10 || got[1].WeeklyPct != 25 || got[1].WeeklyResetAt != "w1" || !got[1].Time.Equal(now.Add(-time.Hour)) {
		t.Errorf("readings = %+v", got)
	}
}
