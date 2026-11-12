package ingest

import (
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readGzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCompressInactive(t *testing.T) {
	dir := t.TempDir()
	rc := NewReceiver(dir, []string{"k"}, &memOffsets{})
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	write := func(rel, content string, mod time.Time) string {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
		os.Chtimes(p, mod, mod)
		return p
	}
	inactive := write("office/projects/-p/s1.jsonl", "{\"n\":1}\n", old)
	subagent := write("office/projects/-p/s1/subagents/a.jsonl", "{\"s\":1}\n", old)
	active := write("office/projects/-p/s2.jsonl", "{\"n\":2}\n", time.Now())
	other := write("office/notes/x.jsonl", "{}\n", old)         // not a session file
	hidden := write("office/projects/.p/s3.jsonl", "{}\n", old) // hidden project

	stats, err := rc.CompressInactive(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Before != 16 || stats.After == 0 {
		t.Errorf("stats = %+v, want 2 files of 16 bytes", stats)
	}
	for _, p := range []string{inactive, subagent} {
		if exists(p) {
			t.Errorf("%s still present", p)
		}
		info, err := os.Stat(p + ".gz")
		if err != nil {
			t.Fatalf("%s.gz: %v", p, err)
		}
		if !info.ModTime().Equal(old) {
			t.Errorf("%s.gz mtime = %s, want the original %s", p, info.ModTime(), old)
		}
	}
	if readGzip(t, inactive+".gz") != "{\"n\":1}\n" {
		t.Error("compressed content differs")
	}
	for _, p := range []string{active, other, hidden} {
		if !exists(p) || exists(p+".gz") {
			t.Errorf("%s should be left alone", p)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(inactive))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}

	// Nothing more to do.
	if stats, err := rc.CompressInactive(24 * time.Hour); err != nil || stats.Files != 0 {
		t.Errorf("second run: %+v, %v", stats, err)
	}
}

func TestUploadsToCompressedFiles(t *testing.T) {
	dir := t.TempDir()
	offsets := &memOffsets{}
	rc := NewReceiver(dir, []string{"k"}, offsets)
	const p = "projects/-p/s1.jsonl"
	file := filepath.Join(dir, "m", "projects", "-p", "s1.jsonl")
	up := func(c chunkReq) int {
		c.token, c.machine, c.path = "k", "m", p
		code, _ := send(t, rc, c)
		return code
	}
	if code := up(chunkReq{offset: 0, end: 2, body: "a\n"}); code != http.StatusOK {
		t.Fatalf("first chunk: %d", code)
	}
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(file, old, old)
	if stats, _ := rc.CompressInactive(24 * time.Hour); stats.Files != 1 {
		t.Fatalf("not compressed: %+v", stats)
	}

	// An agent starting up checks its offset with an empty chunk: the
	// compressed file is left alone.
	if code := up(chunkReq{offset: 2, end: 2}); code != http.StatusOK {
		t.Fatalf("empty check: %d", code)
	}
	if exists(file) || !exists(file+".gz") {
		t.Error("an empty check touched the compressed file")
	}

	// The session resumes: the file is restored, then appended to.
	if code := up(chunkReq{offset: 2, end: 4, body: "b\n"}); code != http.StatusOK {
		t.Fatalf("append: %d", code)
	}
	if exists(file + ".gz") {
		t.Error("compressed copy left next to the restored file")
	}
	if data, _ := os.ReadFile(file); string(data) != "a\nb\n" {
		t.Errorf("restored file = %q, want %q", data, "a\nb\n")
	}

	// A reset replaces a compressed copy.
	os.Chtimes(file, old, old)
	rc.CompressInactive(24 * time.Hour)
	if code := up(chunkReq{offset: 0, end: 2, reset: true, body: "z\n"}); code != http.StatusOK {
		t.Fatalf("reset: %d", code)
	}
	if exists(file + ".gz") {
		t.Error("reset left the compressed copy")
	}
	if data, _ := os.ReadFile(file); string(data) != "z\n" {
		t.Errorf("after reset = %q", data)
	}
}
