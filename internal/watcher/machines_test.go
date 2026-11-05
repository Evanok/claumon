package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMachinesLayoutRules(t *testing.T) {
	split := func(rel string) []string {
		if rel == "" {
			return nil
		}
		return strings.Split(rel, "/")
	}
	tests := []struct {
		rel         string
		wantDir     bool
		wantSession bool
	}{
		{"", true, false},
		{"office", true, false},
		{".tmp", false, false},
		{"office/projects", true, false},
		{"office/memory", false, false},
		{"office/projects/-p", true, false},
		{"office/projects/.hidden", false, false},
		{"office/projects/-p/s1.jsonl", false, true},
		{"office/projects/-p/.s1.jsonl.tmp", false, false},
		{"office/projects/-p/.s1.jsonl", false, false},
		{"office/projects/-p/notes.md", false, false},
		{"office/projects/-p/s1/subagents", false, false},
		{"office/other/-p/s1.jsonl", false, false},
		{"office/s1.jsonl", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			parts := split(tt.rel)
			if got := wantMachinesDir(parts); got != tt.wantDir {
				t.Errorf("wantMachinesDir = %v, want %v", got, tt.wantDir)
			}
			if got := isMachinesSessionFile(parts); got != tt.wantSession {
				t.Errorf("isMachinesSessionFile = %v, want %v", got, tt.wantSession)
			}
		})
	}
}

func TestMachinesRel(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "machines")
	w := &Watcher{machinesDir: root}
	tests := []struct {
		path   string
		want   string // parts joined by "/"
		wantOK bool
	}{
		{root, "", true},
		{filepath.Join(root, "office", "projects"), "office/projects", true},
		{filepath.Join(string(filepath.Separator), "srv", "machines-old", "x"), "", false},
		{filepath.Join(string(filepath.Separator), "home", "u", ".claude"), "", false},
	}
	for _, tt := range tests {
		parts, ok := w.machinesRel(tt.path)
		if ok != tt.wantOK || strings.Join(parts, "/") != tt.want {
			t.Errorf("machinesRel(%q) = %v, %v; want %q, %v", tt.path, parts, ok, tt.want, tt.wantOK)
		}
	}

	if _, ok := (&Watcher{}).machinesRel(root); ok {
		t.Error("machinesRel must report false when multi-machine mode is off")
	}
}

// dispatchRecorder collects dispatched paths from the watcher goroutine.
type dispatchRecorder struct {
	mu       sync.Mutex
	sessions map[string]bool
	memories []string
}

func (r *dispatchRecorder) session(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[path] = true
}

func (r *dispatchRecorder) memory(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memories = append(r.memories, path)
}

// waitSession waits until path has been dispatched as a session change.
func (r *dispatchRecorder) waitSession(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ok := r.sessions[path]
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no session dispatch for %s", path)
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatchMachinesDispatchesSessionFiles(t *testing.T) {
	base := t.TempDir()
	claudeDir := filepath.Join(base, "claude")
	machines := filepath.Join(base, "machines")
	existingProject := filepath.Join(machines, "office", "projects", "-p")
	mkdirAll(t, filepath.Join(claudeDir, "projects"))
	mkdirAll(t, existingProject)

	w, err := New(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WatchMachines(machines); err != nil {
		t.Fatal(err)
	}
	rec := &dispatchRecorder{sessions: map[string]bool{}}
	w.OnSessionChange(rec.session)
	w.OnMemoryChange(rec.memory)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// A session file written in a project directory watched from the start.
	s1 := filepath.Join(existingProject, "s1.jsonl")
	writeFile(t, s1, "{}\n")
	rec.waitSession(t, s1)

	// A whole new machine tree created at once (mkdir -p, then a file): the
	// watcher must pick up the nested directories and the file.
	newProject := filepath.Join(machines, "home", "projects", "-q")
	mkdirAll(t, newProject)
	s2 := filepath.Join(newProject, "s2.jsonl")
	writeFile(t, s2, "{}\n")
	rec.waitSession(t, s2)

	// Memory files from other machines are not dispatched.
	mem := filepath.Join(machines, "office", "projects", "-p", "notes.md")
	writeFile(t, mem, "x")
	s3 := filepath.Join(existingProject, "s3.jsonl")
	writeFile(t, s3, "{}\n")
	rec.waitSession(t, s3)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.memories) != 0 {
		t.Errorf("memory dispatches from the machines tree: %v", rec.memories)
	}
}

func TestWatchMachinesFindsFilesInNewTree(t *testing.T) {
	base := t.TempDir()
	machines := filepath.Join(base, "machines")
	mkdirAll(t, machines)

	w, err := New(filepath.Join(base, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.WatchMachines(machines); err != nil {
		t.Fatal(err)
	}

	// Simulate a tree that appeared with its files before being watched,
	// as handled on a directory Create event.
	project := filepath.Join(machines, "home", "projects", "-q")
	mkdirAll(t, filepath.Join(project, "s1", "subagents"))
	writeFile(t, filepath.Join(project, "s1.jsonl"), "{}\n")
	writeFile(t, filepath.Join(project, "notes.md"), "x")

	var found []string
	w.handleMachinesEvent(filepath.Join(machines, "home"), true, func(p string) { found = append(found, p) })
	if len(found) != 1 || found[0] != filepath.Join(project, "s1.jsonl") {
		t.Errorf("found = %v, want only s1.jsonl", found)
	}

	watched := map[string]bool{}
	for _, p := range w.fsw.WatchList() {
		watched[p] = true
	}
	for _, dir := range []string{machines, filepath.Join(machines, "home"), filepath.Join(machines, "home", "projects"), project} {
		if !watched[dir] {
			t.Errorf("%s is not watched", dir)
		}
	}
	if watched[filepath.Join(project, "s1")] {
		t.Error("session subdirectory must not be watched")
	}
}

func TestWatchMachinesMissingDir(t *testing.T) {
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.WatchMachines(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error for a missing machines directory")
	}
}
