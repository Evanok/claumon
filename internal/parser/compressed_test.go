package parser

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeGzip writes lines compressed at path with modification time mod.
func writeGzip(t *testing.T, path string, lines []string, mod time.Time) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(strings.Join(lines, "\n") + "\n"))
	zw.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestCompressedSessionFiles(t *testing.T) {
	mod := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	lines := []string{ledgerUser1, ledgerResp1, ledgerUser2, ledgerResp2}
	sub := []string{subagentUser, subagentResp}

	// The same data, plain in one tree and compressed in the other.
	plain, compressed := t.TempDir(), t.TempDir()
	writeLedgerFiles(t, plain, []ledgerFile{
		{"claude/projects/-p/s1.jsonl", lines, mod},
		{"claude/projects/-p/s1/subagents/agent-a.jsonl", sub, mod},
	})
	writeGzip(t, filepath.Join(compressed, "claude/projects/-p/s1.jsonl.gz"), lines, mod)
	writeGzip(t, filepath.Join(compressed, "claude/projects/-p/s1/subagents/agent-a.jsonl.gz"), sub, mod)
	setMachinesDirForTest(t, "")

	type result struct {
		Sessions []*SessionSummary
		Daily    map[string]SessionAggregate
		Detail   []SessionMessage
	}
	collect := func(base string) result {
		t.Helper()
		claude := filepath.Join(base, "claude")
		sessions, err := DiscoverSessions(claude)
		if err != nil {
			t.Fatal(err)
		}
		daily, err := DiscoverDailyAggregates(claude)
		if err != nil {
			t.Fatal(err)
		}
		path := FindSessionFile(claude, "s1")
		if path == "" {
			t.Fatalf("%s: session file not found", base)
		}
		detail, err := ParseSessionDetail(path)
		if err != nil {
			t.Fatal(err)
		}
		return result{sessions, daily, detail}
	}
	a, b := collect(plain), collect(compressed)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("compressed files read differently:\nplain      %s\ncompressed %s", ja, jb)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].ID != "s1" || b.Sessions[0].InputTokens != 127 {
		t.Errorf("compressed sessions = %+v", b.Sessions)
	}
}

func TestPlainAndCompressedCopiesMerge(t *testing.T) {
	// While a compressed session is being restored, both files can exist for
	// a moment: they are copies of one session, counted once.
	base := t.TempDir()
	mod := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	lines := []string{ledgerUser1, ledgerResp1}
	writeLedgerFiles(t, base, []ledgerFile{{"claude/projects/-p/s1.jsonl", lines, mod}})
	writeGzip(t, filepath.Join(base, "claude/projects/-p/s1.jsonl.gz"), lines, mod)
	setMachinesDirForTest(t, "")

	sessions, err := DiscoverSessions(filepath.Join(base, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].InputTokens != 100 || sessions[0].MessageCount != 2 {
		t.Errorf("sessions = %+v, want one session counted once", sessions)
	}
}
