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

	"github.com/fabioconcina/claumon/internal/store"
)

// recordingWriter records the dates written and can fail chosen dates.
type recordingWriter struct {
	writes []store.DailyAggregate
	fail   map[string]bool
}

func (w *recordingWriter) UpsertDailyAggregate(agg store.DailyAggregate) error {
	if w.fail[agg.Date] {
		return errors.New("write failed")
	}
	w.writes = append(w.writes, agg)
	return nil
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
