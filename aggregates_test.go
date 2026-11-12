package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
	"github.com/fabioconcina/claumon/internal/store"
)

// recordingWriter records the dates written and can fail chosen dates. It
// keeps the stored message counts like the real store.
type recordingWriter struct {
	writes    []store.DailyAggregate
	fail      map[string]bool
	stored    map[string]int
	countsErr error

	machineWrites []store.MachineDay
	machineStored map[store.MachineDay]int
}

func (w *recordingWriter) UpsertMachineDailyAggregate(machine string, agg store.DailyAggregate) error {
	k := store.MachineDay{Date: agg.Date, Machine: machine}
	w.machineWrites = append(w.machineWrites, k)
	if w.machineStored == nil {
		w.machineStored = map[store.MachineDay]int{}
	}
	w.machineStored[k] = agg.MessageCount
	return nil
}

func (w *recordingWriter) MachineDailyMessageCounts() (map[store.MachineDay]int, error) {
	out := map[store.MachineDay]int{}
	for k, v := range w.machineStored {
		out[k] = v
	}
	return out, nil
}

// takeMachineRows returns the machine rows written since the previous call,
// as sorted "date/machine" strings.
func (w *recordingWriter) takeMachineRows() []string {
	var rows []string
	for _, k := range w.machineWrites {
		rows = append(rows, k.Date+"/"+k.Machine)
	}
	w.machineWrites = nil
	sort.Strings(rows)
	return rows
}

func (w *recordingWriter) UpsertDailyAggregate(agg store.DailyAggregate) error {
	if w.fail[agg.Date] {
		return errors.New("write failed")
	}
	w.writes = append(w.writes, agg)
	if w.stored == nil {
		w.stored = map[string]int{}
	}
	w.stored[agg.Date] = agg.MessageCount
	return nil
}

func (w *recordingWriter) DailyMessageCounts() (map[string]int, error) {
	if w.countsErr != nil {
		return nil, w.countsErr
	}
	out := map[string]int{}
	for k, v := range w.stored {
		out[k] = v
	}
	return out, nil
}

// takeDates returns the dates written since the previous call, sorted.
func (w *recordingWriter) takeDates() []string {
	var dates []string
	for _, a := range w.writes {
		dates = append(dates, a.Date)
	}
	w.writes = nil
	sort.Strings(dates)
	return dates
}

// exchange returns a user line and an assistant line at t, with IDs derived
// from id so each exchange is a distinct record.
func exchange(id string, t time.Time, input int) string {
	ts := t.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{"type":"user","uuid":"u-%[1]s","timestamp":"%[2]s","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a-%[1]s","timestamp":"%[2]s","message":{"id":"msg-%[1]s","model":"claude-sonnet-4-6","usage":{"input_tokens":%[3]d,"output_tokens":1}}}
`, id, ts, input)
}

// localNoon returns noon local time, daysAgo days before today.
func localNoon(daysAgo int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()-daysAgo, 12, 0, 0, 0, time.Local)
}

func writeTranscript(t *testing.T, claudeDir, project, id, content string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// subagentResponse returns a subagent transcript line with one API response
// at t and no message of the session.
func subagentResponse(id string, t time.Time) string {
	ts := t.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{"type":"assistant","uuid":"sub-%[1]s","timestamp":"%[2]s","message":{"id":"msg-sub-%[1]s","model":"claude-sonnet-4-6","usage":{"input_tokens":5,"output_tokens":1}}}
`, id, ts)
}

func TestDailyAggregateSync(t *testing.T) {
	today := localNoon(0).Format("2006-01-02")
	d2 := localNoon(2).Format("2006-01-02")
	d5 := localNoon(5).Format("2006-01-02")

	claudeDir := t.TempDir()
	writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(5), 10)+exchange("2", localNoon(2), 20))
	writeTranscript(t, claudeDir, "-p", "s2", exchange("3", localNoon(0), 30))

	w := &recordingWriter{}
	s := newDailyAggregateSync(claudeDir, w)

	steps := []struct {
		name   string
		change func(t *testing.T)
		want   []string
	}{
		{"first sync writes every day", func(*testing.T) {}, []string{d5, d2, today}},
		{"no change writes nothing", func(*testing.T) {}, nil},
		{
			"record added to a past day rewrites only that day",
			func(t *testing.T) {
				// A copy of s1 in another project directory adds a new
				// exchange dated two days ago and repeats the old ones.
				writeTranscript(t, claudeDir, "-moved", "s1",
					exchange("1", localNoon(5), 10)+exchange("2", localNoon(2), 20)+exchange("4", localNoon(2), 40))
			},
			[]string{d2},
		},
		{
			"day that disappears from the files is not rewritten",
			func(t *testing.T) {
				if err := os.RemoveAll(filepath.Join(claudeDir, "projects", "-p", "s1.jsonl")); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(claudeDir, "projects", "-moved", "s1.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
			nil,
		},
	}
	for _, st := range steps {
		st.change(t)
		s.sync()
		got := w.takeDates()
		if strings.Join(got, ",") != strings.Join(st.want, ",") {
			t.Errorf("%s: wrote %v, want %v", st.name, got, st.want)
		}
	}
}

func TestDailyAggregateSyncKeepsPartlyDeletedDays(t *testing.T) {
	today := localNoon(0).Format("2006-01-02")
	d3 := localNoon(3).Format("2006-01-02")

	claudeDir := t.TempDir()
	s1 := writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(3), 10))
	writeTranscript(t, claudeDir, "-p", "s2", exchange("2", localNoon(3), 20))
	writeTranscript(t, claudeDir, "-p", "s3", exchange("3", localNoon(0), 30))

	w := &recordingWriter{}
	s := newDailyAggregateSync(claudeDir, w)

	steps := []struct {
		name   string
		change func(t *testing.T)
		want   []string
	}{
		{"first sync writes every day", func(*testing.T) {}, []string{d3, today}},
		{
			"transcript of a past day deleted: stored totals kept",
			func(t *testing.T) {
				if err := os.Remove(s1); err != nil {
					t.Fatal(err)
				}
			},
			nil,
		},
		{
			"more tokens with the same messages: written unless the day is partly deleted",
			func(t *testing.T) {
				// Subagent transcripts add tokens, not messages: today is
				// rewritten, the partly deleted day stays kept.
				writeTranscript(t, claudeDir, "-p/s3/subagents", "agent-a", subagentResponse("a", localNoon(0)))
				writeTranscript(t, claudeDir, "-p/s2/subagents", "agent-b", subagentResponse("b", localNoon(3)))
			},
			[]string{today},
		},
		{
			"day with more messages than stored is written",
			func(t *testing.T) {
				writeTranscript(t, claudeDir, "-p", "s4", exchange("4", localNoon(3), 40)+exchange("5", localNoon(3), 50))
			},
			[]string{d3},
		},
	}
	for _, st := range steps {
		st.change(t)
		s.sync()
		got := w.takeDates()
		if strings.Join(got, ",") != strings.Join(st.want, ",") {
			t.Errorf("%s: wrote %v, want %v", st.name, got, st.want)
		}
	}
	if got := w.stored[d3]; got != 6 {
		t.Errorf("stored messages for %s = %d, want 6", d3, got)
	}

	// After a restart the rule applies against what the store holds.
	if err := os.Remove(filepath.Join(claudeDir, "projects", "-p", "s4.jsonl")); err != nil {
		t.Fatal(err)
	}
	restarted := newDailyAggregateSync(claudeDir, w)
	restarted.sync()
	if got := w.takeDates(); strings.Join(got, ",") != today {
		t.Errorf("after restart: wrote %v, want only [%s]", got, today)
	}
}

func TestDailyAggregateSyncNeedsStoredCounts(t *testing.T) {
	claudeDir := t.TempDir()
	writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(1), 10))

	w := &recordingWriter{countsErr: errors.New("db unavailable")}
	s := newDailyAggregateSync(claudeDir, w)
	if n := s.sync(); n != 0 || len(w.writes) != 0 {
		t.Fatalf("sync without stored counts wrote %d days, want 0", n)
	}
	w.countsErr = nil
	if n := s.sync(); n != 1 {
		t.Errorf("retry wrote %d days, want 1", n)
	}
}

func TestDailyAggregateSyncMachines(t *testing.T) {
	today := localNoon(0).Format("2006-01-02")
	d2 := localNoon(2).Format("2006-01-02")

	base := t.TempDir()
	claudeDir := filepath.Join(base, "claude")
	machines := filepath.Join(base, "machines")
	writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(2), 10))
	office := filepath.Join(machines, "office")
	s2 := writeTranscript(t, office, "-p", "s2", exchange("2", localNoon(2), 20)+exchange("3", localNoon(0), 30))

	// Single-machine mode writes no machine rows.
	w := &recordingWriter{}
	newDailyAggregateSync(claudeDir, w).sync()
	if rows := w.takeMachineRows(); len(rows) != 0 {
		t.Fatalf("single-machine mode wrote machine rows %v", rows)
	}

	parser.SetMachinesDir(machines)
	parser.SetMachineNames("laptop", map[string]string{"office": "work"})
	t.Cleanup(func() {
		parser.SetMachinesDir("")
		parser.SetMachineNames("", nil)
	})

	w = &recordingWriter{}
	s := newDailyAggregateSync(claudeDir, w)
	steps := []struct {
		name   string
		change func(t *testing.T)
		want   []string
	}{
		{"first sync writes every machine day", func(*testing.T) {}, []string{d2 + "/laptop", d2 + "/work", today + "/work"}},
		{"no change writes nothing", func(*testing.T) {}, nil},
		{
			"a machine's records added to a past day",
			func(t *testing.T) {
				writeTranscript(t, office, "-p", "s4", exchange("4", localNoon(2), 40))
			},
			[]string{d2 + "/work"},
		},
		{
			"a machine's transcript deleted: its stored days are kept",
			func(t *testing.T) {
				if err := os.Remove(s2); err != nil {
					t.Fatal(err)
				}
			},
			nil,
		},
	}
	for _, st := range steps {
		st.change(t)
		s.sync()
		if got := w.takeMachineRows(); strings.Join(got, ",") != strings.Join(st.want, ",") {
			t.Errorf("%s: wrote %v, want %v", st.name, got, st.want)
		}
	}
	if got := w.machineStored[store.MachineDay{Date: d2, Machine: "work"}]; got != 4 {
		t.Errorf("stored messages for %s/work = %d, want 4", d2, got)
	}
}

func TestDailyAggregateSyncRetriesFailedWrites(t *testing.T) {
	d1 := localNoon(1).Format("2006-01-02")
	claudeDir := t.TempDir()
	writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(1), 10))

	w := &recordingWriter{fail: map[string]bool{d1: true}}
	s := newDailyAggregateSync(claudeDir, w)
	if n := s.sync(); n != 0 {
		t.Fatalf("first sync wrote %d days, want 0 (write fails)", n)
	}
	w.fail = nil
	s.sync()
	if got := w.takeDates(); strings.Join(got, ",") != d1 {
		t.Errorf("retry wrote %v, want [%s]", got, d1)
	}
}

func TestDailyAggregateSyncWithStore(t *testing.T) {
	d2 := localNoon(2).Format("2006-01-02")
	claudeDir := t.TempDir()
	writeTranscript(t, claudeDir, "-p", "s1", exchange("1", localNoon(2), 10))

	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s := newDailyAggregateSync(claudeDir, st)
	s.sync()

	// The session continues in a copy that adds tokens on the same past day.
	writeTranscript(t, claudeDir, "-moved", "s1", exchange("1", localNoon(2), 10)+exchange("2", localNoon(2), 5))
	s.sync()

	history, err := st.GetHistory(7)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range history {
		if h.Date != d2 {
			continue
		}
		found = true
		if h.InputTokens != 15 || h.MessageCount != 4 || h.SessionCount != 1 {
			t.Errorf("%s: input/messages/sessions = %d/%d/%d, want 15/4/1", d2, h.InputTokens, h.MessageCount, h.SessionCount)
		}
	}
	if !found {
		t.Fatalf("no history row for %s in %+v", d2, history)
	}
}
