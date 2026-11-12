package parser

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// A subagent exchange of session s1, run on another model.
const (
	subagentUser = `{"type":"user","uuid":"su1","timestamp":"2026-03-20T10:30:00Z","isSidechain":true,"message":{"role":"user","content":"look this up"}}`
	subagentResp = `{"type":"assistant","uuid":"sa1","timestamp":"2026-03-20T10:30:05Z","isSidechain":true,"message":{"id":"msg_sub_1","model":"claude-haiku-4-5","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":50,"cache_creation_input_tokens":5}}}`
)

func TestSubagentTranscriptsAddToTotals(t *testing.T) {
	mod := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	mainCost := math.Round(costFor("claude-sonnet-4-6", 100, 10, 1000, 1)*10000) / 10000
	wantCost := math.Round((mainCost+costFor("claude-haiku-4-5", 7, 3, 50, 5))*10000) / 10000

	tests := []struct {
		name     string
		machines bool
		files    []ledgerFile
	}{
		{
			name: "one copy",
			files: []ledgerFile{
				{"claude/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1}, mod},
				{"claude/projects/-p/s1/subagents/agent-a.jsonl", []string{subagentUser, subagentResp}, mod},
			},
		},
		{
			// The session and its subagent transcript collected from a second
			// machine: every record is counted once.
			name:     "two copies",
			machines: true,
			files: []ledgerFile{
				{"claude/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1}, mod},
				{"claude/projects/-p/s1/subagents/agent-a.jsonl", []string{subagentUser, subagentResp}, mod},
				{"machines/home/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1}, mod.Add(time.Hour)},
				{"machines/home/projects/-p/s1/subagents/agent-a.jsonl", []string{subagentUser, subagentResp}, mod.Add(time.Hour)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			writeLedgerFiles(t, base, tt.files)
			mdir := ""
			if tt.machines {
				mdir = filepath.Join(base, "machines")
			}
			setMachinesDirForTest(t, mdir)
			claude := filepath.Join(base, "claude")

			for _, discover := range []struct {
				name string
				fn   func() ([]*SessionSummary, error)
			}{
				{"DiscoverSessions", func() ([]*SessionSummary, error) { return DiscoverSessions(claude) }},
				{"DiscoverRecentSessions", func() ([]*SessionSummary, error) { return DiscoverRecentSessions(claude, 1) }},
			} {
				sessions, err := discover.fn()
				if err != nil {
					t.Fatal(err)
				}
				if len(sessions) != 1 || sessions[0].ID != "s1" {
					t.Fatalf("%s: sessions = %+v, want only s1 (a subagent is not a session)", discover.name, sessions)
				}
				s := sessions[0]
				if s.InputTokens != 107 || s.OutputTokens != 13 || s.CacheReadTokens != 1050 || s.CacheCreateTokens != 6 {
					t.Errorf("%s: tokens in/out/read/create = %d/%d/%d/%d, want 107/13/1050/6", discover.name,
						s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreateTokens)
				}
				if s.MessageCount != 2 {
					t.Errorf("%s: messages = %d, want 2 (subagent lines are not session messages)", discover.name, s.MessageCount)
				}
				if s.EstimatedCostUSD != wantCost {
					t.Errorf("%s: cost = %v, want %v (subagent priced at its own model)", discover.name, s.EstimatedCostUSD, wantCost)
				}
			}

			daily, err := DiscoverDailyAggregates(claude)
			if err != nil {
				t.Fatal(err)
			}
			d := daily[day(20)]
			if d.InputTokens != 107 || d.OutputTokens != 13 || d.CacheReadTokens != 1050 || d.CacheCreateTokens != 6 {
				t.Errorf("daily tokens in/out/read/create = %d/%d/%d/%d, want 107/13/1050/6",
					d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheCreateTokens)
			}
			if d.MessageCount != 2 || d.SessionCount != 1 {
				t.Errorf("daily messages/sessions = %d/%d, want 2/1", d.MessageCount, d.SessionCount)
			}
			if math.Abs(d.CostUSD-wantCost) > 0.0001 {
				t.Errorf("daily cost = %v, want %v", d.CostUSD, wantCost)
			}

			files, err := enumerateFiles(claude)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, n := range hourlyTokens(files.all(), day(20)) {
				total += n
			}
			if total != 107+13+1050+6 {
				t.Errorf("hourly total = %d, want %d", total, 107+13+1050+6)
			}
		})
	}
}

func TestSubagentWithoutSessionFile(t *testing.T) {
	// The session file is gone but its subagent transcript remains: the
	// tokens still count in the daily totals, for the parent session, and
	// no session row is made from the subagent.
	base := t.TempDir()
	mod := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		{"claude/projects/-p/s9/subagents/agent-a.jsonl", []string{subagentUser, subagentResp}, mod},
	})
	setMachinesDirForTest(t, "")
	claude := filepath.Join(base, "claude")

	sessions, err := DiscoverSessions(claude)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none", sessions)
	}
	daily, err := DiscoverDailyAggregates(claude)
	if err != nil {
		t.Fatal(err)
	}
	if d := daily[day(20)]; d.InputTokens != 7 || d.SessionCount != 1 || d.MessageCount != 0 {
		t.Errorf("daily input/sessions/messages = %d/%d/%d, want 7/1/0", d.InputTokens, d.SessionCount, d.MessageCount)
	}
}
