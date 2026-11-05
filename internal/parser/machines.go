package parser

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// machinesDir is the optional root of session data collected from other
// machines, laid out as <machinesDir>/<machine>/projects/<project>/*.jsonl
// (the same layout as a local ~/.claude). Empty means single-machine mode.
var machinesDir atomic.Value // string

// SetMachinesDir enables multi-machine discovery: session files found under
// dir/<machine>/projects are reported alongside the local ones, tagged with
// their machine name. An empty dir restores single-machine behavior.
func SetMachinesDir(dir string) {
	machinesDir.Store(dir)
}

func getMachinesDir() string {
	dir, _ := machinesDir.Load().(string)
	return dir
}

// projectsRoot is one directory laid out like ~/.claude/projects, together
// with the machine its files come from ("" for the local machine).
type projectsRoot struct {
	machine string
	dir     string
}

// projectsRoots returns the local projects directory followed by one entry
// per machine under the machines directory, in directory order. Hidden
// entries and machines without a projects/ directory are skipped. An
// unreadable machines directory yields only the local root.
func projectsRoots(claudeDir string) []projectsRoot {
	roots := []projectsRoot{{machine: "", dir: filepath.Join(claudeDir, "projects")}}

	mdir := getMachinesDir()
	if mdir == "" {
		return roots
	}
	entries, err := os.ReadDir(mdir)
	if err != nil {
		return roots
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(mdir, name, "projects")
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, projectsRoot{machine: name, dir: dir})
	}
	return roots
}
