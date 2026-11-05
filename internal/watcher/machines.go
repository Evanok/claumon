package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Multi-machine mode: session files collected from other machines live under
// <machinesDir>/<machine>/projects/<project>/*.jsonl. Only the directories of
// that layout are watched, and only session files are dispatched from it
// (memory files of other machines are not shown by the dashboard).

// WatchMachines adds the machines directory tree to the watch list. It must
// be called before Start.
func (w *Watcher) WatchMachines(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("watching machines directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("watching machines directory: %s is not a directory", dir)
	}
	w.machinesDir = filepath.Clean(dir)
	w.addMachinesTree(w.machinesDir, nil)
	return nil
}

// machinesRel returns the path elements of path below the machines
// directory, nil for the directory itself, and ok=false when path is
// outside it or multi-machine mode is off.
func (w *Watcher) machinesRel(path string) (parts []string, ok bool) {
	if w.machinesDir == "" {
		return nil, false
	}
	rel, err := filepath.Rel(w.machinesDir, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false
	}
	if rel == "." {
		return nil, true
	}
	return strings.Split(rel, string(filepath.Separator)), true
}

// wantMachinesDir reports whether a directory at the given position below the
// machines directory belongs to the watched layout: the root, a machine, its
// projects directory, or a project. Hidden entries are skipped, as the parser
// does.
func wantMachinesDir(parts []string) bool {
	for _, p := range parts {
		if strings.HasPrefix(p, ".") {
			return false
		}
	}
	switch len(parts) {
	case 0, 1:
		return true
	case 2, 3:
		return parts[1] == "projects"
	default:
		return false
	}
}

// addMachinesTree watches dir and its subdirectories that belong to the
// layout. Session files already present in project directories are passed to
// found, so files written before their directory was watched are not missed.
func (w *Watcher) addMachinesTree(dir string, found func(path string)) {
	parts, ok := w.machinesRel(dir)
	if !ok || !wantMachinesDir(parts) {
		return
	}
	w.fsw.Add(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		if e.IsDir() {
			w.addMachinesTree(child, found)
			continue
		}
		if found != nil && isMachinesSessionFile(append(parts[:len(parts):len(parts)], e.Name())) {
			found(child)
		}
	}
}

// handleMachinesEvent processes an event under the machines directory and
// reports whether it did; trigger debounces a session file dispatch.
func (w *Watcher) handleMachinesEvent(path string, created bool, trigger func(string)) bool {
	parts, ok := w.machinesRel(path)
	if !ok {
		return false
	}
	if created {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			w.addMachinesTree(path, trigger)
			return true
		}
	}
	if isMachinesSessionFile(parts) {
		trigger(path)
	}
	return true
}

// isMachinesSessionFile reports whether parts locate a session file:
// <machine>/projects/<project>/<name>.jsonl, the files the parser reads.
func isMachinesSessionFile(parts []string) bool {
	return len(parts) == 4 && wantMachinesDir(parts[:3]) &&
		strings.HasSuffix(parts[3], ".jsonl") && !strings.HasPrefix(parts[3], ".")
}
