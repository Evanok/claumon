package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/store"
)

func TestMachineTracker(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr := newMachineTracker(st)
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	seen := func() time.Time {
		t.Helper()
		statuses, err := st.MachineStatuses()
		if err != nil {
			t.Fatal(err)
		}
		return statuses["m"].LastSeen
	}
	tr.MachineSeen("m", t0, false)
	tr.MachineSeen("m", t0.Add(30*time.Second), false) // within a minute: not written
	if got := seen(); !got.Equal(t0) {
		t.Errorf("seen = %s, want %s (throttled)", got, t0)
	}
	tr.MachineSeen("m", t0.Add(40*time.Second), true) // data: always written
	statuses, _ := st.MachineStatuses()
	if s := statuses["m"]; !s.LastSeen.Equal(t0.Add(40*time.Second)) || !s.LastData.Equal(t0.Add(40*time.Second)) {
		t.Errorf("after data: %+v", s)
	}
	tr.MachineSeen("m", t0.Add(2*time.Minute), false)
	if got := seen(); !got.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("seen = %s, want a write after a minute", got)
	}
}
