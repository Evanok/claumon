package ingest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testServer runs a Receiver behind an HTTP server.
type testServer struct {
	dir     string
	offsets *memOffsets
	http    *httptest.Server
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{dir: t.TempDir(), offsets: &memOffsets{}}
	ts.http = httptest.NewServer(NewReceiver(ts.dir, []string{"tok"}, ts.offsets))
	t.Cleanup(ts.http.Close)
	return ts
}

func writeSrc(t *testing.T, claude, rel, content string) {
	t.Helper()
	p := filepath.Join(claude, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendSrc(t *testing.T, claude, rel, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(claude, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
}

// stored returns the server's copy of a file of machine "m".
func (ts *testServer) stored(rel string) string {
	data, _ := os.ReadFile(filepath.Join(ts.dir, "m", filepath.FromSlash(rel)))
	return string(data)
}

func newAgent(claude, url string) *Agent {
	return &Agent{ClaudeDir: claude, ServerURL: url, Token: "tok", Machine: "m", State: LoadState("")}
}

const (
	s1  = "projects/-home-a-p/s1.jsonl"
	sub = "projects/-home-a-p/s1/subagents/agent-a.jsonl"
	s2  = "projects/C--Users-User-work/s2.jsonl"
)

func TestAgentSync(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	claude := t.TempDir()
	writeSrc(t, claude, s1, "{\"n\":1}\n{\"n\":2}\n{\"n\":3, \"incomp") // last line still being written
	writeSrc(t, claude, sub, "{\"sub\":1}\n")
	writeSrc(t, claude, s2, "{\"w\":1}\n")
	writeSrc(t, claude, "projects/.hidden/x.jsonl", "{}\n")
	writeSrc(t, claude, "projects/-home-a-p/notes.md", "x\n")
	writeSrc(t, claude, "projects/-home-a-p/memory/m.jsonl", "{}\n")

	a := newAgent(claude, srv.http.URL)
	stats, err := a.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 {
		t.Errorf("files sent = %d, want 3", stats.Files)
	}
	if got := srv.stored(s1); got != "{\"n\":1}\n{\"n\":2}\n" {
		t.Errorf("s1 = %q, want the complete lines only", got)
	}
	if srv.stored(sub) != "{\"sub\":1}\n" || srv.stored(s2) != "{\"w\":1}\n" {
		t.Errorf("subagent/windows files = %q / %q", srv.stored(sub), srv.stored(s2))
	}
	if _, err := os.Stat(filepath.Join(srv.dir, "m", "projects", ".hidden")); err == nil {
		t.Error("hidden project sent")
	}

	// Nothing new: nothing sent.
	if stats, err := a.Sync(ctx); err != nil || stats.Files != 0 {
		t.Errorf("idle sync: %+v, %v", stats, err)
	}

	// The last line completes and more lines follow.
	appendSrc(t, claude, s1, "plete\"}\n{\"n\":4}\n")
	if _, err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(claude, filepath.FromSlash(s1)))
	if srv.stored(s1) != string(src) {
		t.Errorf("after append: s1 = %q, want %q", srv.stored(s1), src)
	}
}

func TestAgentKeepsModificationTime(t *testing.T) {
	srv := newTestServer(t)
	claude := t.TempDir()
	writeSrc(t, claude, s1, "{\"n\":1}\n")
	mtime := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(claude, filepath.FromSlash(s1)), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := newAgent(claude, srv.http.URL).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(srv.dir, "m", filepath.FromSlash(s1)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mtime) {
		t.Errorf("copy mtime = %s, want the source's %s", info.ModTime(), mtime)
	}
}

func TestAgentSmallChunks(t *testing.T) {
	srv := newTestServer(t)
	claude := t.TempDir()
	var content strings.Builder
	for i := 0; i < 50; i++ {
		content.WriteString(`{"line":"` + strings.Repeat("x", i) + "\"}\n")
	}
	writeSrc(t, claude, s1, content.String())

	a := newAgent(claude, srv.http.URL)
	a.ChunkBytes = 16 // shorter than most lines
	if _, err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.stored(s1) != content.String() {
		t.Errorf("stored %d bytes, want %d", len(srv.stored(s1)), content.Len())
	}
}

func TestAgentRecovers(t *testing.T) {
	ctx := context.Background()
	claude := t.TempDir()
	writeSrc(t, claude, s1, "{\"n\":1}\n{\"n\":2}\n")

	t.Run("server lost its state", func(t *testing.T) {
		srv := newTestServer(t)
		a := newAgent(claude, srv.http.URL)
		a.State.SetOffset(s1, 16) // the agent believes everything was sent
		if _, err := a.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		if srv.stored(s1) != "{\"n\":1}\n{\"n\":2}\n" {
			t.Errorf("s1 = %q, want the whole file resent", srv.stored(s1))
		}
	})

	t.Run("agent lost its state", func(t *testing.T) {
		srv := newTestServer(t)
		if _, err := newAgent(claude, srv.http.URL).Sync(ctx); err != nil {
			t.Fatal(err)
		}
		appendSrc(t, claude, s1, "{\"n\":3}\n")
		if _, err := newAgent(claude, srv.http.URL).Sync(ctx); err != nil {
			t.Fatal(err)
		}
		if srv.stored(s1) != "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n" {
			t.Errorf("s1 = %q, want no duplicate", srv.stored(s1))
		}
	})

	t.Run("source rewritten shorter", func(t *testing.T) {
		srv := newTestServer(t)
		a := newAgent(claude, srv.http.URL)
		if _, err := a.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		writeSrc(t, claude, s1, "{\"new\":1}\n")
		if _, err := a.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		if srv.stored(s1) != "{\"new\":1}\n" {
			t.Errorf("s1 = %q, want the rewritten file", srv.stored(s1))
		}
	})

	t.Run("server unreachable", func(t *testing.T) {
		srv := newTestServer(t)
		srv.http.Close()
		a := newAgent(claude, srv.http.URL)
		if _, err := a.Sync(ctx); err == nil {
			t.Error("sync to a closed server succeeded")
		}
		if a.State.Offset(s1) != 0 {
			t.Error("offset advanced without a successful upload")
		}
	})
}

func TestAgentFilter(t *testing.T) {
	srv := newTestServer(t)
	claude := t.TempDir()
	writeSrc(t, claude, s1, "keep 1\ndrop me\nkeep 2\n")

	a := newAgent(claude, srv.http.URL)
	a.Filter = func(line []byte) []byte {
		if bytes.HasPrefix(line, []byte("drop")) {
			return nil
		}
		return bytes.ToUpper(line)
	}
	stats, err := a.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if srv.stored(s1) != "KEEP 1\nKEEP 2\n" {
		t.Errorf("s1 = %q", srv.stored(s1))
	}
	if stats.SourceSent != 22 || stats.BytesSent != 14 {
		t.Errorf("stats = %+v, want 22 source bytes, 14 sent", stats)
	}
	// Offsets stay in source terms.
	if got := a.State.Offset(s1); got != 22 {
		t.Errorf("state offset = %d, want 22", got)
	}
}

func TestStateSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-state.json")
	s := LoadState(path)
	s.SetOffset(s1, 42)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path).Offset(s1); got != 42 {
		t.Errorf("reloaded offset = %d, want 42", got)
	}
	os.WriteFile(path, []byte("not json"), 0o600)
	if got := LoadState(path).Offset(s1); got != 0 {
		t.Errorf("corrupt state: offset = %d, want 0", got)
	}
}

func TestRetryWait(t *testing.T) {
	interval := 30 * time.Minute
	for failures, want := range map[int]time.Duration{
		1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 5: 16 * time.Minute, 6: 30 * time.Minute, 20: 30 * time.Minute,
	} {
		if got := retryWait(failures, interval); got != want {
			t.Errorf("retryWait(%d) = %s, want %s", failures, got, want)
		}
	}
	if got := retryWait(3, 90*time.Second); got != 90*time.Second {
		t.Errorf("short interval: %s", got)
	}
}

func TestReplyErrorHidesHTML(t *testing.T) {
	for body, want := range map[string]string{
		"<html><body><h1>502 Bad Gateway</h1></body></html>": "server replied 502 Bad Gateway",
		"invalid ingest token\n":                             "server replied 502 Bad Gateway: invalid ingest token",
		"":                                                   "server replied 502 Bad Gateway",
	} {
		resp := &http.Response{Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader(body))}
		if got := replyError(resp).Error(); got != want {
			t.Errorf("body %q: %q, want %q", body, got, want)
		}
	}
}
