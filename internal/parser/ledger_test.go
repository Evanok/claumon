package parser

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Records for cross-file deduplication tests. Each response has a single
// line, so its usage is counted once per distinct message ID.
const (
	ledgerUser1 = `{"type":"user","uuid":"u1","timestamp":"2026-03-20T10:00:00Z","message":{"role":"user","content":"hi"}}`
	ledgerResp1 = `{"type":"assistant","uuid":"a1","timestamp":"2026-03-20T10:00:05Z","message":{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":1}}}`
	ledgerUser2 = `{"type":"user","uuid":"u2","timestamp":"2026-03-20T11:00:00Z","message":{"role":"user","content":"more"}}`
	ledgerResp2 = `{"type":"assistant","uuid":"a2","timestamp":"2026-03-20T11:00:05Z","message":{"id":"msg_2","model":"claude-sonnet-4-6","usage":{"input_tokens":20,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
	// Next-day continuation, only present in the copy.
	ledgerUser3 = `{"type":"user","uuid":"u3","timestamp":"2026-03-21T09:00:00Z","message":{"role":"user","content":"next day"}}`
	ledgerResp3 = `{"type":"assistant","uuid":"a3","timestamp":"2026-03-21T09:00:05Z","message":{"id":"msg_3","model":"claude-sonnet-4-6","usage":{"input_tokens":3,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
	// Legacy records with neither a line UUID nor a message ID.
	ledgerLegacyUser = `{"type":"user","timestamp":"2026-03-20T12:00:00Z","message":{"role":"user","content":"old"}}`
	ledgerLegacyResp = `{"type":"assistant","timestamp":"2026-03-20T12:00:05Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
)

// ledgerFile describes one session file of a test tree.
type ledgerFile struct {
	rel     string // path relative to the tree root
	lines   []string
	modTime time.Time
}

// writeLedgerFiles writes the files under root with their modification times.
func writeLedgerFiles(t *testing.T, root string, files []ledgerFile) {
	t.Helper()
	for _, f := range files {
		path := filepath.Join(root, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(f.lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, f.modTime, f.modTime); err != nil {
			t.Fatal(err)
		}
	}
}

func day(d int) string {
	return time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
}

func TestDiscoverDailyAggregatesCrossFileDedup(t *testing.T) {
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)

	type dayWant struct {
		input, output, cacheRead, cacheCreate, messages, sessions int
	}
	tests := []struct {
		name     string
		machines bool
		files    []ledgerFile
		want     map[string]dayWant
	}{
		{
			name: "single file unchanged",
			files: []ledgerFile{
				{"claude/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, older},
			},
			want: map[string]dayWant{
				day(20): {120, 12, 1000, 1, 4, 1},
			},
		},
		{
			name: "session copied to another project directory",
			files: []ledgerFile{
				{"claude/projects/-old/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, older},
				{"claude/projects/-new/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2, ledgerUser3, ledgerResp3}, newer},
			},
			want: map[string]dayWant{
				day(20): {120, 12, 1000, 1, 4, 1},
				day(21): {3, 3, 0, 0, 2, 1},
			},
		},
		{
			name: "copy resumed under a new session ID",
			files: []ledgerFile{
				{"claude/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1}, older},
				{"claude/projects/-p/s2.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser3, ledgerResp3}, newer},
			},
			// Day 20 records are attributed to s1, the older file, so s2 only
			// counts as a session on day 21.
			want: map[string]dayWant{
				day(20): {100, 10, 1000, 1, 2, 1},
				day(21): {3, 3, 0, 0, 2, 1},
			},
		},
		{
			name:     "same session on two machines",
			machines: true,
			files: []ledgerFile{
				{"machines/office/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, older},
				{"machines/home/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2, ledgerUser3, ledgerResp3}, newer},
			},
			want: map[string]dayWant{
				day(20): {120, 12, 1000, 1, 4, 1},
				day(21): {3, 3, 0, 0, 2, 1},
			},
		},
		{
			name: "records without IDs are never deduplicated",
			files: []ledgerFile{
				{"claude/projects/-old/s1.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, older},
				{"claude/projects/-new/s1.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, newer},
			},
			want: map[string]dayWant{
				day(20): {2, 2, 0, 0, 4, 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			writeLedgerFiles(t, base, tt.files)
			claude := filepath.Join(base, "claude")
			if err := os.MkdirAll(filepath.Join(claude, "projects"), 0o755); err != nil {
				t.Fatal(err)
			}
			mdir := ""
			if tt.machines {
				mdir = filepath.Join(base, "machines")
			}
			setMachinesDirForTest(t, mdir)

			got, err := DiscoverDailyAggregates(claude)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d days %v, want %d", len(got), got, len(tt.want))
			}
			for date, w := range tt.want {
				g, ok := got[date]
				if !ok {
					t.Fatalf("missing day %s in %v", date, got)
				}
				gotRow := fmt.Sprint(g.InputTokens, g.OutputTokens, g.CacheReadTokens, g.CacheCreateTokens, g.MessageCount, g.SessionCount)
				wantRow := fmt.Sprint(w.input, w.output, w.cacheRead, w.cacheCreate, w.messages, w.sessions)
				if gotRow != wantRow {
					t.Errorf("%s: got in/out/cr/cc/msgs/sessions %s, want %s", date, gotRow, wantRow)
				}
				wantCost := math.Round(costFor("claude-sonnet-4-6", w.input, w.output, w.cacheRead, w.cacheCreate)*10000) / 10000
				if math.Abs(g.CostUSD-wantCost) > 1e-9 {
					t.Errorf("%s: CostUSD = %v, want %v", date, g.CostUSD, wantCost)
				}
			}
		})
	}
}

func TestHourlyTokensCrossFileDedup(t *testing.T) {
	base := t.TempDir()
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		{"a/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, older},
		{"b/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2, ledgerUser3, ledgerResp3}, older.Add(time.Hour)},
	})
	var files []sessionFile
	for _, rel := range []string{"a/s1.jsonl", "b/s1.jsonl"} {
		f, err := singleFile(filepath.Join(base, rel))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}

	hours := hourlyTokens(files, day(20), nil)
	h1 := time.Date(2026, 3, 20, 10, 0, 5, 0, time.UTC).Local().Hour()
	h2 := time.Date(2026, 3, 20, 11, 0, 5, 0, time.UTC).Local().Hour()
	want := [24]int{}
	want[h1] += 100 + 10 + 1000 + 1
	want[h2] += 20 + 2
	if hours != want {
		t.Errorf("hours = %v, want %v", hours, want)
	}
}

func TestVisitUniqueRecordsAttributesToOldestFile(t *testing.T) {
	base := t.TempDir()
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	// The newer file sorts first by path, so attribution must come from
	// modification time, not from enumeration order.
	writeLedgerFiles(t, base, []ledgerFile{
		{"a/s1.jsonl", []string{ledgerUser1, ledgerResp1}, older.Add(time.Hour)},
		{"z/s1.jsonl", []string{ledgerUser1, ledgerResp1}, older},
	})
	var files []sessionFile
	for _, rel := range []string{"a/s1.jsonl", "z/s1.jsonl"} {
		f, err := singleFile(filepath.Join(base, rel))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}

	owners := map[string]string{}
	visitUniqueRecords(files, ledgerVisitor{
		response: func(f sessionFile, r apiResponse) { owners[r.ID] = filepath.Base(filepath.Dir(f.path)) },
		message:  func(f sessionFile, m ledgerMessage) { owners[m.UUID] = filepath.Base(filepath.Dir(f.path)) },
	})
	for _, key := range []string{"msg_1", "u1", "a1"} {
		if owners[key] != "z" {
			t.Errorf("%s attributed to %q, want %q (oldest file)", key, owners[key], "z")
		}
	}
	if len(owners) != 3 {
		t.Errorf("owners = %v, want 3 records", owners)
	}
}
