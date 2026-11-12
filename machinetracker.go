package main

import (
	"log"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/store"
)

// machineTracker records agents' contacts in the store (see
// ingest.MachineTracker). Agents ask about the usage gauges every 30
// seconds, so contacts without data are written at most once a minute per
// machine; contacts with data are always written.
type machineTracker struct {
	st *store.Store

	mu        sync.Mutex
	lastWrite map[string]time.Time
}

const machineSeenEvery = time.Minute

func newMachineTracker(st *store.Store) *machineTracker {
	return &machineTracker{st: st, lastWrite: map[string]time.Time{}}
}

func (t *machineTracker) MachineSeen(machine string, at time.Time, gotData bool) {
	t.mu.Lock()
	if !gotData && at.Sub(t.lastWrite[machine]) < machineSeenEvery {
		t.mu.Unlock()
		return
	}
	t.lastWrite[machine] = at
	t.mu.Unlock()
	if err := t.st.TouchMachine(machine, at, gotData); err != nil {
		log.Printf("[ingest] %v", err)
	}
}
