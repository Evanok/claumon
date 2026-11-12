package store

import (
	"strings"
	"testing"
	"time"
)

func TestMachineAggregates(t *testing.T) {
	st := openTestStore(t)
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	old := time.Now().AddDate(0, 0, -100).Format("2006-01-02")

	machines, err := st.Machines()
	if err != nil {
		t.Fatal(err)
	}
	if machines == nil || len(machines) != 0 {
		t.Errorf("empty store: machines = %#v, want an empty non-nil slice", machines)
	}

	for _, w := range []struct {
		machine string
		agg     DailyAggregate
	}{
		{"office", DailyAggregate{Date: yesterday, InputTokens: 10, MessageCount: 4, CostUSD: 1.5}},
		{"home", DailyAggregate{Date: yesterday, InputTokens: 20, MessageCount: 6}},
		{"office", DailyAggregate{Date: today, InputTokens: 30, MessageCount: 2}},
		{"office", DailyAggregate{Date: today, InputTokens: 35, MessageCount: 3}}, // overwrites
		{"home", DailyAggregate{Date: old, InputTokens: 99, MessageCount: 9}},
	} {
		if err := st.UpsertMachineDailyAggregate(w.machine, w.agg); err != nil {
			t.Fatal(err)
		}
	}

	machines, err = st.Machines()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(machines, ","); got != "home,office" {
		t.Errorf("machines = %s, want home,office", got)
	}

	history, err := st.GetMachineHistory("office", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history has %d days, want 3 (zero-filled)", len(history))
	}
	if h := history[1]; h.Date != yesterday || h.InputTokens != 10 || h.CostUSD != 1.5 {
		t.Errorf("office yesterday = %+v", h)
	}
	if h := history[2]; h.Date != today || h.InputTokens != 35 || h.MessageCount != 3 {
		t.Errorf("office today = %+v, want the overwritten row", h)
	}
	if h := history[0]; h.InputTokens != 0 {
		t.Errorf("office two days ago = %+v, want zero", h)
	}

	counts, err := st.MachineDailyMessageCounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 4 || counts[MachineDay{yesterday, "home"}] != 6 || counts[MachineDay{today, "office"}] != 3 {
		t.Errorf("counts = %v", counts)
	}

	// Pruning applies to machine rows too; daily_aggregates is untouched by
	// machine writes.
	if err := st.Prune(90); err != nil {
		t.Fatal(err)
	}
	counts, err = st.MachineDailyMessageCounts()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := counts[MachineDay{old, "home"}]; ok || len(counts) != 3 {
		t.Errorf("after prune: counts = %v, want the 100-day-old row gone", counts)
	}
	if n := aggregateCount(t, st); n != 0 {
		t.Errorf("daily_aggregates has %d rows, want 0", n)
	}
}

func TestMachineStatus(t *testing.T) {
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	statuses, err := st.MachineStatuses()
	if err != nil || len(statuses) != 0 {
		t.Fatalf("empty store: %v, %v", statuses, err)
	}
	for _, c := range []struct {
		machine string
		at      time.Time
		data    bool
	}{
		{"office", t0, true},
		{"office", t0.Add(time.Hour), false},   // seen later, nothing new
		{"office", t0.Add(-time.Hour), true},   // an older report does not go back
		{"home", t0.Add(2 * time.Hour), false}, // seen, never sent data
	} {
		if err := st.TouchMachine(c.machine, c.at, c.data); err != nil {
			t.Fatal(err)
		}
	}
	statuses, err = st.MachineStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses["office"]; !s.LastSeen.Equal(t0.Add(time.Hour)) || !s.LastData.Equal(t0) {
		t.Errorf("office = %+v, want seen %s, data %s", s, t0.Add(time.Hour), t0)
	}
	if s := statuses["home"]; !s.LastSeen.Equal(t0.Add(2*time.Hour)) || !s.LastData.IsZero() {
		t.Errorf("home = %+v, want seen without data", s)
	}
}
