package parser

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/memory"
)

func TestDiscoverSessionsMergesCopies(t *testing.T) {
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	partial := []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}
	full := []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2, ledgerUser3, ledgerResp3}

	type want struct {
		project      string // project directory name, before decoding
		machine      string
		machines     string // comma-joined
		input        int
		messageCount int
		start, last  string // RFC 3339, UTC
	}
	tests := []struct {
		name     string
		machines bool
		files    []ledgerFile
		want     map[string]want // by session ID
	}{
		{
			name: "single copy is unchanged",
			files: []ledgerFile{
				{"claude/projects/-p/s1.jsonl", partial, older},
			},
			want: map[string]want{
				"s1": {"-p", "", "", 120, 4, "2026-03-20T10:00:00Z", "2026-03-20T11:00:05Z"},
			},
		},
		{
			name: "copy in another project directory gives one row",
			files: []ledgerFile{
				{"claude/projects/-old/s1.jsonl", partial, older},
				{"claude/projects/-new/s1.jsonl", full, newer},
				{"claude/projects/-new/s2.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, older},
			},
			want: map[string]want{
				"s1": {"-new", "", "", 123, 6, "2026-03-20T10:00:00Z", "2026-03-21T09:00:05Z"},
				"s2": {"-new", "", "", 1, 2, "2026-03-20T12:00:00Z", "2026-03-20T12:00:05Z"},
			},
		},
		{
			name:     "same session on two machines and locally",
			machines: true,
			files: []ledgerFile{
				{"machines/office/projects/-p/s1.jsonl", full, newer},
				{"machines/home/projects/-p/s1.jsonl", partial, older},
				{"claude/projects/-p/s1.jsonl", partial, older},
				{"machines/home/projects/-p/s3.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, older},
			},
			want: map[string]want{
				"s1": {"-p", "office", "home,office", 123, 6, "2026-03-20T10:00:00Z", "2026-03-21T09:00:05Z"},
				"s3": {"-p", "home", "home", 1, 2, "2026-03-20T12:00:00Z", "2026-03-20T12:00:05Z"},
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

			sessions, err := DiscoverSessions(claude)
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != len(tt.want) {
				t.Fatalf("got %d sessions, want %d", len(sessions), len(tt.want))
			}
			for _, s := range sessions {
				w, ok := tt.want[s.ID]
				if !ok {
					t.Fatalf("unexpected session %q", s.ID)
				}
				if s.Project != memory.DecodePath(w.project) || s.Machine != w.machine || strings.Join(s.Machines, ",") != w.machines {
					t.Errorf("%s: project/machine/machines = %q/%q/%v, want %q/%q/%q",
						s.ID, s.Project, s.Machine, s.Machines, w.project, w.machine, w.machines)
				}
				if s.InputTokens != w.input || s.MessageCount != w.messageCount {
					t.Errorf("%s: input/messages = %d/%d, want %d/%d", s.ID, s.InputTokens, s.MessageCount, w.input, w.messageCount)
				}
				if got := s.StartTime.UTC().Format(time.RFC3339); got != w.start {
					t.Errorf("%s: start = %s, want %s", s.ID, got, w.start)
				}
				if got := s.LastActivity.UTC().Format(time.RFC3339); got != w.last {
					t.Errorf("%s: last activity = %s, want %s", s.ID, got, w.last)
				}
				if wantCost := estimateCost(s); s.EstimatedCostUSD != wantCost {
					t.Errorf("%s: cost = %v, want %v", s.ID, s.EstimatedCostUSD, wantCost)
				}
			}
		})
	}
}

func TestDiscoverRecentSessionsIncludesOlderCopies(t *testing.T) {
	base := t.TempDir()
	t0 := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		// s1's older copy is the oldest file, outside a limit of 2 files.
		{"claude/projects/-old/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, t0},
		{"claude/projects/-p/s2.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, t0.Add(time.Hour)},
		{"claude/projects/-new/s1.jsonl", []string{ledgerUser3, ledgerResp3}, t0.Add(2 * time.Hour)},
		{"claude/projects/-p/s3.jsonl", []string{ledgerUser2, ledgerResp2}, t0.Add(3 * time.Hour)},
	})
	setMachinesDirForTest(t, "")

	sessions, err := DiscoverRecentSessions(filepath.Join(base, "claude"), 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.ID)
		if s.ID == "s1" && s.InputTokens != 123 {
			t.Errorf("s1 input = %d, want 123 (older copy merged)", s.InputTokens)
		}
	}
	// s1 has the latest activity (2026-03-21), then s3 (shares msg_2 with s1
	// but is a distinct session).
	if got := strings.Join(ids, ","); got != "s1,s3" {
		t.Errorf("sessions = %s, want s1,s3", got)
	}
}

func TestDiscoverRecentSessionsLimitsBySession(t *testing.T) {
	base := t.TempDir()
	t0 := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		{"claude/projects/-p/s3.jsonl", []string{ledgerUser2, ledgerResp2}, t0},
		{"claude/projects/-p/s2.jsonl", []string{ledgerLegacyUser, ledgerLegacyResp}, t0.Add(time.Hour)},
		// The two most recent files are copies of s1: they must take one
		// place of the limit, not two.
		{"claude/projects/-old/s1.jsonl", []string{ledgerUser1, ledgerResp1}, t0.Add(2 * time.Hour)},
		{"machines/home/projects/-old/s1.jsonl", []string{ledgerUser1, ledgerResp1}, t0.Add(3 * time.Hour)},
	})
	setMachinesDirForTest(t, filepath.Join(base, "machines"))

	sessions, err := DiscoverRecentSessions(filepath.Join(base, "claude"), 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	if got := strings.Join(ids, ","); got != "s1,s2" {
		t.Errorf("sessions = %s, want s1,s2", got)
	}
}

func TestFindSessionFileReturnsPrimaryCopy(t *testing.T) {
	base := t.TempDir()
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		{"claude/projects/-a/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser3, ledgerResp3}, older},
		{"claude/projects/-b/s1.jsonl", []string{ledgerUser1, ledgerResp1}, older.Add(time.Hour)},
		{"machines/office/projects/-c/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}, older},
	})
	setMachinesDirForTest(t, filepath.Join(base, "machines"))

	// The -a copy has the latest activity in its records, even though its
	// file is not the most recently modified one.
	got := FindSessionFile(filepath.Join(base, "claude"), "s1")
	if want := filepath.Join(base, "claude", "projects", "-a", "s1.jsonl"); got != want {
		t.Errorf("FindSessionFile = %q, want %q", got, want)
	}
}

func TestPrimaryCopyTieBreaks(t *testing.T) {
	at := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	mk := func(path string, size int64, last time.Time) sessionCopy {
		return sessionCopy{file: sessionFile{path: path, size: size}, summary: SessionSummary{LastActivity: last}}
	}
	tests := []struct {
		name   string
		copies []sessionCopy
		want   string
	}{
		{"latest activity wins", []sessionCopy{mk("/a", 900, at), mk("/b", 10, at.Add(time.Second))}, "/b"},
		{"then largest file", []sessionCopy{mk("/a", 10, at), mk("/b", 900, at)}, "/b"},
		{"then smallest path", []sessionCopy{mk("/b", 10, at), mk("/a", 10, at)}, "/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.copies[primaryCopy(tt.copies)].file.path; got != tt.want {
				t.Errorf("primary = %s, want %s", got, tt.want)
			}
		})
	}
}
