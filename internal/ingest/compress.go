package ingest

import (
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Files received from agents can be compressed once their session is
// inactive: <name>.jsonl becomes <name>.jsonl.gz, which the parser reads the
// same way (about ten times smaller). When an agent sends more lines for a
// compressed file (a resumed session), the receiver restores it first. The
// receiver's lock keeps compression and uploads of a file apart.

// CompressStats summarizes one CompressInactive run.
type CompressStats struct {
	Files  int
	Before int64 // bytes before compression
	After  int64 // bytes after
}

// CompressInactive compresses the session files under the machines
// directory not modified for olderThan. The copies keep their modification
// time. It stops at the first error, keeping what was done.
func (rc *Receiver) CompressInactive(olderThan time.Duration) (CompressStats, error) {
	var stats CompressStats
	cutoff := time.Now().Add(-olderThan)
	var candidates []string
	err := filepath.WalkDir(rc.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable or vanished: skip
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		rel, err := filepath.Rel(rc.dir, path)
		if err != nil {
			return nil
		}
		machine, file, ok := strings.Cut(filepath.ToSlash(rel), "/")
		if !ok || !ValidMachineName(machine) || !ValidPath(file) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	for _, path := range candidates {
		before, after, done, err := rc.compressIfInactive(path, cutoff)
		if err != nil {
			return stats, err
		}
		if done {
			stats.Files++
			stats.Before += before
			stats.After += after
		}
	}
	return stats, nil
}

// compressIfInactive compresses path under the receiver's lock, unless it
// was modified since the scan or already has a compressed copy.
func (rc *Receiver) compressIfInactive(path string, cutoff time.Time) (before, after int64, done bool, err error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Before(cutoff) {
		return 0, 0, false, nil
	}
	if _, err := os.Stat(path + ".gz"); err == nil {
		return 0, 0, false, nil
	}
	if err := transcode(path, path+".gz", info.ModTime(), true); err != nil {
		return 0, 0, false, err
	}
	gz, err := os.Stat(path + ".gz")
	if err != nil {
		return 0, 0, false, err
	}
	if err := os.Remove(path); err != nil {
		return 0, 0, false, err
	}
	return info.Size(), gz.Size(), true, nil
}

// restoreCompressed turns target.gz back into target, before an append.
// It does nothing when target.gz does not exist.
func restoreCompressed(target string) error {
	info, err := os.Stat(target + ".gz")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := transcode(target+".gz", target, info.ModTime(), false); err != nil {
		return err
	}
	return os.Remove(target + ".gz")
}

// transcode writes src to dst, compressing or decompressing, through a
// hidden temporary file renamed into place, and gives dst the modification
// time mod.
func transcode(src, dst string, mod time.Time, compress bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp")
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		out.Close()
		os.Remove(tmp)
		return err
	}

	if compress {
		zw := gzip.NewWriter(out)
		if _, err := io.Copy(zw, in); err != nil {
			return fail(err)
		}
		if err := zw.Close(); err != nil {
			return fail(err)
		}
	} else {
		zr, err := gzip.NewReader(in)
		if err != nil {
			return fail(err)
		}
		if _, err := io.Copy(out, zr); err != nil {
			return fail(err)
		}
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chtimes(tmp, mod, mod); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
