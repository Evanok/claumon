package ingest

import "time"

// MachineTracker records agents' contacts, so the dashboard can show when
// each machine was last seen and last sent new session data.
type MachineTracker interface {
	MachineSeen(machine string, at time.Time, gotData bool)
}

// SetMachineTracker makes the receiver report agents' contacts to t.
func (rc *Receiver) SetMachineTracker(t MachineTracker) {
	rc.tracker = t
}

// seen reports a contact from machine when it names a valid machine.
func (rc *Receiver) seen(machine string, gotData bool) {
	if rc.tracker != nil && ValidMachineName(machine) {
		rc.tracker.MachineSeen(machine, time.Now(), gotData)
	}
}
