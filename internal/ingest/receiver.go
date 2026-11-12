// Package ingest moves session files from the machines where Claude Code runs
// to a claumon server in multi-machine mode. An agent on each machine sends
// the new complete lines of its session files; the server appends them under
// <machines_dir>/<machine>/<path>, the layout the parser reads.
//
// Protocol: POST /api/ingest, one chunk of one file per request.
//
//	Authorization: Bearer <token>       one of the server's ingest_tokens
//	X-Claumon-Machine: <machine>        machine name, see ValidMachineName
//	X-Claumon-Path: <path>              file path relative to the Claude
//	                                    directory, see ValidPath
//	X-Claumon-Offset: <n>               offset in the source file where the
//	                                    chunk starts
//	X-Claumon-End: <n>                  offset in the source file where it
//	                                    ends (the agent may filter lines, so
//	                                    the body can be shorter)
//	X-Claumon-Reset: 1                  optional: the source file shrank;
//	                                    replace the stored file (Offset is 0)
//	X-Claumon-Mtime: <unix nanoseconds> optional: modification time of the
//	                                    source file, given to the copy
//	Content-Encoding: gzip              optional
//
// The server keeps, per machine and file, the source offset received so far.
// A chunk must start there: otherwise the reply is 409 with the expected
// offset, and the agent resends from it. A successful reply is 200 with the
// new offset. Both bodies are {"offset": n}.
//
// The copy keeps the source's modification time: the parser attributes a
// record found in several copies of a session to the copy modified first,
// the original, and arrival times would make that depend on upload order.
package ingest

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Header names of the protocol.
const (
	HeaderMachine = "X-Claumon-Machine"
	HeaderPath    = "X-Claumon-Path"
	HeaderOffset  = "X-Claumon-Offset"
	HeaderEnd     = "X-Claumon-End"
	HeaderReset   = "X-Claumon-Reset"
	HeaderMtime   = "X-Claumon-Mtime"
)

// Limits on one request: the compressed body as sent, and the chunk once
// decompressed. Agents send chunks well under MaxChunkBytes.
const (
	MaxBodyBytes  = 16 << 20
	MaxChunkBytes = 64 << 20
)

// OffsetStore keeps the source offset received per machine and file.
type OffsetStore interface {
	IngestOffset(machine, path string) (int64, error)
	SetIngestOffset(machine, path string, offset int64) error
}

// Receiver is the server side of the protocol.
type Receiver struct {
	dir    string // the machines directory
	tokens []string
	store  OffsetStore

	mu sync.Mutex // one chunk at a time: chunks of a file must apply in order
}

// NewReceiver returns a receiver writing under dir (the machines directory)
// and accepting the given tokens.
func NewReceiver(dir string, tokens []string, store OffsetStore) *Receiver {
	return &Receiver{dir: dir, tokens: tokens, store: store}
}

var machineNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidMachineName reports whether name can be a machine directory name:
// 1 to 64 letters, digits, '.', '_' or '-', not starting with '.', '_' or
// '-'.
func ValidMachineName(name string) bool {
	return machineNameRE.MatchString(name)
}

// ValidPath reports whether p is the path of a session file the parser reads,
// relative to a Claude directory, with '/' separators:
// projects/<project>/<name>.jsonl or
// projects/<project>/<session>/subagents/<name>.jsonl. No element may be
// empty, start with '.' or contain a backslash.
func ValidPath(p string) bool {
	parts := strings.Split(p, "/")
	for _, e := range parts {
		if e == "" || strings.HasPrefix(e, ".") || strings.ContainsAny(e, `\:`) {
			return false
		}
	}
	if parts[0] != "projects" || !strings.HasSuffix(parts[len(parts)-1], ".jsonl") {
		return false
	}
	switch len(parts) {
	case 3:
		return true
	case 5:
		return parts[3] == "subagents"
	default:
		return false
	}
}

type offsetReply struct {
	Offset int64 `json:"offset"`
}

func reply(w http.ResponseWriter, status int, offset int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(offsetReply{Offset: offset})
}

func (rc *Receiver) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	for _, t := range rc.tokens {
		if t != "" && subtle.ConstantTimeCompare([]byte(token), []byte(t)) == 1 {
			return true
		}
	}
	return false
}

// ServeHTTP handles POST /api/ingest.
func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !rc.authorized(r) {
		http.Error(w, "invalid ingest token", http.StatusUnauthorized)
		return
	}

	machine := r.Header.Get(HeaderMachine)
	relPath := r.Header.Get(HeaderPath)
	offset, errOffset := strconv.ParseInt(r.Header.Get(HeaderOffset), 10, 64)
	end, errEnd := strconv.ParseInt(r.Header.Get(HeaderEnd), 10, 64)
	reset := r.Header.Get(HeaderReset) == "1"
	var mtime time.Time
	if v := r.Header.Get(HeaderMtime); v != "" {
		ns, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ns <= 0 {
			http.Error(w, "invalid modification time", http.StatusBadRequest)
			return
		}
		mtime = time.Unix(0, ns)
	}
	switch {
	case !ValidMachineName(machine):
		http.Error(w, "invalid machine name", http.StatusBadRequest)
		return
	case !ValidPath(relPath):
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	case errOffset != nil || errEnd != nil || offset < 0 || end < offset:
		http.Error(w, "invalid offsets", http.StatusBadRequest)
		return
	case reset && offset != 0:
		http.Error(w, "a reset starts at offset 0", http.StatusBadRequest)
		return
	}

	chunk, err := readChunk(r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	stored, err := rc.store.IngestOffset(machine, relPath)
	if err != nil {
		log.Printf("[ingest] %v", err)
		http.Error(w, "reading ingest state failed", http.StatusInternalServerError)
		return
	}
	if !reset && offset != stored {
		reply(w, http.StatusConflict, stored)
		return
	}

	if err := rc.write(machine, relPath, chunk, reset, mtime); err != nil {
		log.Printf("[ingest] Failed to write %s/%s: %v", machine, relPath, err)
		http.Error(w, "writing the file failed", http.StatusInternalServerError)
		return
	}
	// Recorded after the write: if claumon stops in between, the agent resends
	// the chunk and its lines are stored twice, which the parser's
	// deduplication counts once.
	if err := rc.store.SetIngestOffset(machine, relPath, end); err != nil {
		log.Printf("[ingest] %v", err)
		http.Error(w, "writing ingest state failed", http.StatusInternalServerError)
		return
	}
	reply(w, http.StatusOK, end)
}

var errTooLarge = errors.New("chunk too large")

// readChunk reads the request body, decompressing it when gzip-encoded,
// within the size limits.
func readChunk(r *http.Request) ([]byte, error) {
	body := io.Reader(http.MaxBytesReader(nil, r.Body, MaxBodyBytes))
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		defer zr.Close()
		body = zr
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxChunkBytes+1))
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		return nil, errTooLarge
	case err != nil:
		return nil, fmt.Errorf("reading the body: %w", err)
	case len(data) > MaxChunkBytes:
		return nil, errTooLarge
	}
	return data, nil
}

// write appends chunk to the machine's copy of the file, or replaces the copy
// when reset is set, then gives it mtime when set. A compressed copy is
// restored before an append and dropped by a reset. An empty chunk that is
// not a reset (an agent checking its offset) leaves the file alone.
func (rc *Receiver) write(machine, relPath string, chunk []byte, reset bool, mtime time.Time) error {
	if len(chunk) == 0 && !reset {
		return nil
	}
	target := filepath.Join(rc.dir, machine, filepath.FromSlash(path.Clean(relPath)))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if reset {
		if err := os.Remove(target + ".gz"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := restoreCompressed(target); err != nil {
		return fmt.Errorf("restoring the compressed copy: %w", err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if reset {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(target, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(chunk); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if !mtime.IsZero() {
		return os.Chtimes(target, mtime, mtime)
	}
	return nil
}
