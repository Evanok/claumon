package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultChunkBytes is the source size an agent reads per request.
const DefaultChunkBytes = 4 << 20

// Agent pushes the session files of one machine to a claumon server. Each
// Sync sends the complete lines appended to every session and subagent
// transcript since the previous one. Claude Code only appends to these
// files, so an offset per file is enough to know what is new; the server
// holds the authoritative offsets and the agent's State only saves a round
// trip per file. The first Sync of an Agent checks every file's offset with
// the server, so files the server lost are sent again.
type Agent struct {
	ClaudeDir string // the machine's Claude directory (~/.claude)
	ServerURL string // base URL of the claumon server
	Token     string
	Machine   string

	// Filter, when set, rewrites each complete line (newline included)
	// before it is sent; returning nil drops the line.
	Filter func(line []byte) []byte

	Client     *http.Client
	State      *State
	ChunkBytes int // source bytes per request; DefaultChunkBytes when 0

	verified bool // a full Sync has checked every offset with the server
}

// SyncStats summarizes one Sync.
type SyncStats struct {
	Files      int   // files with new data sent
	SourceSent int64 // source bytes covered
	BytesSent  int64 // bytes sent after filtering, before compression
}

// Sync sends what is new in every session file. It stops at the first
// network or server error, keeping the progress made so far in State.
func (a *Agent) Sync(ctx context.Context) (SyncStats, error) {
	var stats SyncStats
	files, err := a.sessionFiles()
	if err != nil {
		return stats, err
	}
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		sent, kept, err := a.syncFile(ctx, rel, !a.verified)
		if sent > 0 {
			stats.Files++
			stats.SourceSent += sent
			stats.BytesSent += kept
		}
		if err != nil {
			return stats, fmt.Errorf("%s: %w", rel, err)
		}
	}
	a.verified = true
	return stats, nil
}

// sessionFiles lists the session and subagent transcripts under the Claude
// directory, as protocol paths, sorted.
func (a *Agent) sessionFiles() ([]string, error) {
	root := filepath.Join(a.ClaudeDir, "projects")
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	var files []string
	for _, pattern := range []string{"*/*.jsonl", "*/*/subagents/*.jsonl"} {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(a.ClaudeDir, m)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if ValidPath(rel) {
				files = append(files, rel)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// syncFile sends the new complete lines of one file, chunk by chunk. With
// verify, a file believed fully sent is checked with an empty chunk, which
// the server answers with its own offset when it differs. It returns the
// source bytes covered and the bytes kept after filtering.
func (a *Agent) syncFile(ctx context.Context, rel string, verify bool) (sent, kept int64, err error) {
	src := filepath.Join(a.ClaudeDir, filepath.FromSlash(rel))
	info, err := os.Stat(src)
	if err != nil {
		return 0, 0, nil // deleted meanwhile: nothing to send, never deleted remotely
	}
	size, mtime := info.Size(), info.ModTime()
	offset := a.State.Offset(rel)
	reset := false
	if size < offset {
		// The source shrank (rewritten): replace the server's copy.
		offset, reset = 0, true
	}

	conflicts := 0
	for offset < size || reset || verify {
		chunk, end, err := readLines(src, offset, size, a.chunkBytes())
		if err != nil {
			return sent, kept, err
		}
		if end == offset && !reset && !verify {
			return sent, kept, nil // only an incomplete last line is new
		}
		verify = false
		body := a.filter(chunk)
		status, serverOffset, err := a.post(ctx, rel, offset, end, reset, body, mtime)
		if err != nil {
			return sent, kept, err
		}
		switch status {
		case http.StatusOK:
			sent += end - offset
			kept += int64(len(body))
			offset, reset = end, false
			a.State.SetOffset(rel, offset)
		case http.StatusConflict:
			// The server holds another offset (state lost on either side):
			// resume from it, or start over when it is past our file.
			if conflicts++; conflicts > 2 {
				return sent, kept, errors.New("server offset keeps changing")
			}
			if serverOffset > size {
				offset, reset = 0, true
			} else {
				offset = serverOffset
			}
			a.State.SetOffset(rel, offset)
		}
	}
	return sent, kept, nil
}

func (a *Agent) chunkBytes() int {
	if a.ChunkBytes > 0 {
		return a.ChunkBytes
	}
	return DefaultChunkBytes
}

// readLines reads the complete lines of path from offset, up to about limit
// bytes (more when a single line is longer) and never past size. It returns
// the data and the offset after it; an incomplete last line is left out.
func readLines(path string, offset, size int64, limit int) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	want := size - offset
	if want > int64(limit) {
		want = int64(limit)
	}
	buf := make([]byte, want)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, offset, err
	}
	buf = buf[:n]
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
		return buf, offset + int64(len(buf)), nil
	}
	// No newline within limit: a line longer than limit, read it whole.
	if int64(n) == size-offset {
		return nil, offset, nil // the incomplete last line
	}
	br := bufio.NewReader(io.NewSectionReader(f, offset, size-offset))
	var line []byte
	var read int64
	tooLong := false
	for {
		part, err := br.ReadSlice('\n')
		read += int64(len(part))
		if !tooLong && len(line)+len(part) <= MaxChunkBytes {
			line = append(line, part...)
		} else {
			tooLong, line = true, nil
		}
		switch {
		case err == nil:
			if tooLong {
				// Longer than the protocol allows: skip the whole line.
				return nil, offset + read, nil
			}
			return line, offset + read, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, offset, nil // the incomplete last line
		default:
			return nil, offset, err
		}
	}
}

// filter applies Filter to each line of chunk.
func (a *Agent) filter(chunk []byte) []byte {
	if a.Filter == nil {
		return chunk
	}
	out := make([]byte, 0, len(chunk))
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		line := chunk[:i+1]
		chunk = chunk[i+1:]
		out = append(out, a.Filter(line)...)
	}
	return out
}

// post sends one chunk and returns the reply status (200 or 409) and the
// server's offset.
func (a *Agent) post(ctx context.Context, rel string, offset, end int64, reset bool, body []byte, mtime time.Time) (int, int64, error) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(body)
	if err := zw.Close(); err != nil {
		return 0, 0, err
	}

	url := strings.TrimSuffix(a.ServerURL, "/") + "/api/ingest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &gz)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set(HeaderMachine, a.Machine)
	req.Header.Set(HeaderPath, path.Clean(rel))
	req.Header.Set(HeaderOffset, strconv.FormatInt(offset, 10))
	req.Header.Set(HeaderEnd, strconv.FormatInt(end, 10))
	if reset {
		req.Header.Set(HeaderReset, "1")
	}
	req.Header.Set(HeaderMtime, strconv.FormatInt(mtime.UnixNano(), 10))

	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, 0, fmt.Errorf("server replied %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var r offsetReply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, 0, fmt.Errorf("decoding the server reply: %w", err)
	}
	return resp.StatusCode, r.Offset, nil
}

// Run syncs every interval until ctx is done. Errors are reported to
// logf and retried at the next interval.
func (a *Agent) Run(ctx context.Context, interval time.Duration, logf func(format string, args ...any)) {
	for {
		stats, err := a.Sync(ctx)
		if saveErr := a.State.Save(); saveErr != nil {
			logf("[agent] Failed to save state: %v", saveErr)
		}
		switch {
		case err != nil && ctx.Err() == nil:
			logf("[agent] Sync stopped: %v (retrying in %s)", err, interval)
		case stats.Files > 0:
			logf("[agent] Sent %d files: %.1f MB of transcripts, %.1f MB after filtering",
				stats.Files, float64(stats.SourceSent)/(1<<20), float64(stats.BytesSent)/(1<<20))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
