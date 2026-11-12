package parser

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A claumon server can compress the session files of inactive sessions it
// received from agents (multi-machine mode): <session>.jsonl becomes
// <session>.jsonl.gz. Both forms are read the same way.

// openSessionFile opens a session file for reading, decompressing a
// .jsonl.gz file.
func openSessionFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return gzipFile{zr, f}, nil
}

// gzipFile closes the gzip reader and the file under it.
type gzipFile struct {
	*gzip.Reader
	f *os.File
}

func (g gzipFile) Close() error {
	err := g.Reader.Close()
	if ferr := g.f.Close(); err == nil {
		err = ferr
	}
	return err
}

// globSessionFiles returns the session files matching pattern + ".jsonl",
// then those matching pattern + ".jsonl.gz".
func globSessionFiles(pattern string) ([]string, error) {
	plain, err := filepath.Glob(pattern + ".jsonl")
	if err != nil {
		return nil, err
	}
	compressed, err := filepath.Glob(pattern + ".jsonl.gz")
	if err != nil {
		return nil, err
	}
	return append(plain, compressed...), nil
}
