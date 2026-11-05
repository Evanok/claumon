package parser

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dupSession has one response (msg_1) logged as two content-block lines that
// repeat the same usage, one response (msg_2) whose last line carries the
// final usage, and one legacy line with no message ID.
const dupSession = `{"type":"user","timestamp":"2026-03-20T10:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-03-20T10:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":10}}}
{"type":"assistant","timestamp":"2026-03-20T10:00:02Z","message":{"id":"msg_1","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"tool_use","name":"Read"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":10}}}
{"type":"assistant","timestamp":"2026-03-20T10:05:00Z","message":{"id":"msg_2","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"assistant","timestamp":"2026-03-20T10:05:01Z","message":{"id":"msg_2","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":5,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"assistant","timestamp":"2026-03-20T10:06:00Z","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"c"}],"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
`

// Deduplicated totals: msg_1 once + msg_2's last line + the ID-less line.
const (
	dupIn          = 100 + 5 + 1
	dupOut         = 50 + 20 + 1
	dupCacheRead   = 1000
	dupCacheCreate = 10
)

func writeDupSession(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dup.jsonl")
	if err := os.WriteFile(path, []byte(dupSession), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSessionFileDedupesResponses(t *testing.T) {
	s, err := ParseSessionFile(writeDupSession(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.InputTokens != dupIn || s.OutputTokens != dupOut ||
		s.CacheReadTokens != dupCacheRead || s.CacheCreateTokens != dupCacheCreate {
		t.Errorf("tokens = in:%d out:%d cr:%d cc:%d, want in:%d out:%d cr:%d cc:%d",
			s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreateTokens,
			dupIn, dupOut, dupCacheRead, dupCacheCreate)
	}
	// Message count is per line, unchanged by this dedupe.
	if s.MessageCount != 6 {
		t.Errorf("MessageCount = %d, want 6", s.MessageCount)
	}
	// Context length still reflects the last assistant line.
	if s.ContextLength != 1 {
		t.Errorf("ContextLength = %d, want 1", s.ContextLength)
	}
}

func TestParseSessionFileDailyDedupesResponses(t *testing.T) {
	buckets, err := ParseSessionFileDaily(writeDupSession(t))
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
	b := buckets[date]
	if b == nil {
		t.Fatalf("no bucket for %s: %v", date, buckets)
	}
	if b.InputTokens != dupIn || b.OutputTokens != dupOut ||
		b.CacheReadTokens != dupCacheRead || b.CacheCreateTokens != dupCacheCreate {
		t.Errorf("bucket tokens = %+v", b)
	}
	want := math.Round(costFor("claude-sonnet-4-6", dupIn, dupOut, dupCacheRead, dupCacheCreate)*10000) / 10000
	if math.Abs(b.CostUSD-want) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", b.CostUSD, want)
	}
}

func TestParseSessionFileHourlyDedupesResponses(t *testing.T) {
	f, err := singleFile(writeDupSession(t))
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
	var total int
	for _, n := range hourlyTokens([]sessionFile{f}, date) {
		total += n
	}
	if want := dupIn + dupOut + dupCacheRead + dupCacheCreate; total != want {
		t.Errorf("hourly total = %d, want %d", total, want)
	}
}
