package ingest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// memOffsets is an in-memory OffsetStore.
type memOffsets struct {
	mu sync.Mutex
	m  map[string]int64
}

func (s *memOffsets) IngestOffset(machine, path string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[machine+"|"+path], nil
}

func (s *memOffsets) SetIngestOffset(machine, path string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]int64{}
	}
	s.m[machine+"|"+path] = offset
	return nil
}

func TestValidMachineName(t *testing.T) {
	for name, want := range map[string]bool{
		"office":                              true,
		"wsl-DESKTOP-OM7HLL9":                 true,
		"host.example_1":                      true,
		"":                                    false,
		".hidden":                             false,
		"-x":                                  false,
		"a/b":                                 false,
		"..":                                  false,
		"a b":                                 false,
		string(bytes.Repeat([]byte("a"), 65)): false,
	} {
		if got := ValidMachineName(name); got != want {
			t.Errorf("ValidMachineName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidPath(t *testing.T) {
	for p, want := range map[string]bool{
		"projects/-home-a-p/s1.jsonl":              true,
		"projects/C--Users-User-work/s1.jsonl":     true,
		"projects/-p/s1/subagents/agent-a.jsonl":   true,
		"projects/-p/s1.json":                      false,
		"projects/s1.jsonl":                        false,
		"projects/-p/s1/other/agent-a.jsonl":       false,
		"projects/-p/s1/subagents/x/agent-a.jsonl": false,
		"other/-p/s1.jsonl":                        false,
		"projects/../etc/s1.jsonl":                 false,
		"projects/-p/../../s1.jsonl":               false,
		"projects/.hidden/s1.jsonl":                false,
		"projects/-p/.s1.jsonl":                    false,
		"/projects/-p/s1.jsonl":                    false,
		"projects//s1.jsonl":                       false,
		`projects/-p\..\x/s1.jsonl`:                false,
		"projects/C:/s1.jsonl":                     false,
		"projects/-p/s1.jsonl/":                    false,
	} {
		if got := ValidPath(p); got != want {
			t.Errorf("ValidPath(%q) = %v, want %v", p, got, want)
		}
	}
}

type chunkReq struct {
	token, machine, path string
	offset, end          int64
	reset, gzip          bool
	body                 string
}

func send(t *testing.T, h http.Handler, c chunkReq) (int, int64) {
	t.Helper()
	body := []byte(c.body)
	if c.gzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(body)
		zw.Close()
		body = buf.Bytes()
	}
	req := httptest.NewRequest("POST", "/api/ingest", bytes.NewReader(body))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(HeaderMachine, c.machine)
	req.Header.Set(HeaderPath, c.path)
	req.Header.Set(HeaderOffset, strconv.FormatInt(c.offset, 10))
	req.Header.Set(HeaderEnd, strconv.FormatInt(c.end, 10))
	if c.reset {
		req.Header.Set(HeaderReset, "1")
	}
	if c.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var r offsetReply
	if w.Code == http.StatusOK || w.Code == http.StatusConflict {
		if err := json.NewDecoder(w.Body).Decode(&r); err != nil {
			t.Fatalf("decoding reply: %v", err)
		}
	}
	return w.Code, r.Offset
}

func TestReceiver(t *testing.T) {
	dir := t.TempDir()
	rc := NewReceiver(dir, []string{"secret-1", "secret-2"}, &memOffsets{})
	const p = "projects/-p/s1.jsonl"
	file := filepath.Join(dir, "office", "projects", "-p", "s1.jsonl")
	ok := func(c chunkReq) chunkReq {
		if c.token == "" {
			c.token = "secret-1"
		}
		if c.machine == "" {
			c.machine = "office"
		}
		if c.path == "" {
			c.path = p
		}
		return c
	}

	steps := []struct {
		name       string
		req        chunkReq
		wantStatus int
		wantOffset int64
		wantFile   string
	}{
		{"no token", chunkReq{machine: "office", path: p, end: 3, body: "a\n"}, http.StatusUnauthorized, 0, ""},
		{"wrong token", chunkReq{token: "nope", machine: "office", path: p, end: 3, body: "a\n"}, http.StatusUnauthorized, 0, ""},
		{"invalid machine", ok(chunkReq{machine: "../x", end: 3, body: "a\n"}), http.StatusBadRequest, 0, ""},
		{"invalid path", ok(chunkReq{path: "projects/../x.jsonl", end: 3, body: "a\n"}), http.StatusBadRequest, 0, ""},
		{"end before offset", ok(chunkReq{offset: 5, end: 2}), http.StatusBadRequest, 0, ""},
		{"reset not at 0", ok(chunkReq{offset: 5, end: 9, reset: true}), http.StatusBadRequest, 0, ""},
		// The agent filtered a 10-byte source chunk down to "a\n".
		{"first chunk", ok(chunkReq{offset: 0, end: 10, body: "a\n"}), http.StatusOK, 10, "a\n"},
		{"second token works too", ok(chunkReq{token: "secret-2", offset: 10, end: 20, body: "b\n", gzip: true}), http.StatusOK, 20, "a\nb\n"},
		{"replayed chunk", ok(chunkReq{offset: 10, end: 20, body: "b\n"}), http.StatusConflict, 20, "a\nb\n"},
		{"gap", ok(chunkReq{offset: 30, end: 40, body: "d\n"}), http.StatusConflict, 20, "a\nb\n"},
		{"empty chunk of dropped lines", ok(chunkReq{offset: 20, end: 25}), http.StatusOK, 25, "a\nb\n"},
		{"source shrank: reset", ok(chunkReq{offset: 0, end: 4, reset: true, body: "z\n"}), http.StatusOK, 4, "z\n"},
		{"after reset", ok(chunkReq{offset: 4, end: 8, body: "y\n"}), http.StatusOK, 8, "z\ny\n"},
	}
	for _, st := range steps {
		code, offset := send(t, rc, st.req)
		if code != st.wantStatus {
			t.Errorf("%s: status = %d, want %d", st.name, code, st.wantStatus)
			continue
		}
		if (code == http.StatusOK || code == http.StatusConflict) && offset != st.wantOffset {
			t.Errorf("%s: offset = %d, want %d", st.name, offset, st.wantOffset)
		}
		data, _ := os.ReadFile(file)
		if string(data) != st.wantFile {
			t.Errorf("%s: file = %q, want %q", st.name, data, st.wantFile)
		}
	}

	// Machines and files are independent.
	if code, offset := send(t, rc, ok(chunkReq{machine: "home", offset: 0, end: 3, body: "h\n"})); code != http.StatusOK || offset != 3 {
		t.Errorf("other machine: %d, %d", code, offset)
	}
	sub := "projects/-p/s1/subagents/agent-a.jsonl"
	if code, _ := send(t, rc, ok(chunkReq{path: sub, offset: 0, end: 3, body: "s\n"})); code != http.StatusOK {
		t.Errorf("subagent file: %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "office", filepath.FromSlash(sub))); string(data) != "s\n" {
		t.Errorf("subagent file = %q", data)
	}
}

func TestReceiverInvalidMtime(t *testing.T) {
	rc := NewReceiver(t.TempDir(), []string{"k"}, &memOffsets{})
	for _, v := range []string{"yesterday", "-5", "0"} {
		req := httptest.NewRequest("POST", "/api/ingest", strings.NewReader("a\n"))
		req.Header.Set("Authorization", "Bearer k")
		req.Header.Set(HeaderMachine, "m")
		req.Header.Set(HeaderPath, "projects/-p/s.jsonl")
		req.Header.Set(HeaderOffset, "0")
		req.Header.Set(HeaderEnd, "2")
		req.Header.Set(HeaderMtime, v)
		w := httptest.NewRecorder()
		rc.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("mtime %q: status = %d, want 400", v, w.Code)
		}
	}
}

func TestReceiverRejectsLargeChunks(t *testing.T) {
	rc := NewReceiver(t.TempDir(), []string{"k"}, &memOffsets{})
	big := string(bytes.Repeat([]byte("x"), MaxChunkBytes+1))
	// Compressed, the body is small; decompressed, it exceeds the chunk limit.
	code, _ := send(t, rc, chunkReq{token: "k", machine: "m", path: "projects/-p/s.jsonl", end: int64(len(big)), body: big, gzip: true})
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", code)
	}
}
