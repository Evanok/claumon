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

// machineNames holds how machines are named in multi-machine mode.
var machineNames atomic.Value // machineNaming

// machineNaming names the machines in multi-machine mode: local is the name
// of the machine claumon runs on, aliases maps a machine name (a hostname,
// or a subdirectory name of the machines directory) to the name to show.
type machineNaming struct {
	local   string
	aliases map[string]string
}

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

// SetMachineNames sets how machines are named in multi-machine mode: local
// is the name of the local machine (typically its hostname), and aliases
// maps machine names to the names to show. Names without an alias are shown
// as they are. In single-machine mode sessions carry no machine name.
func SetMachineNames(local string, aliases map[string]string) {
	copied := make(map[string]string, len(aliases))
	for k, v := range aliases {
		copied[k] = v
	}
	machineNames.Store(machineNaming{local: local, aliases: copied})
}

// LocalMachineName returns the name shown for the machine claumon runs on in
// multi-machine mode (its hostname or alias), or "" in single-machine mode.
func LocalMachineName() string {
	if getMachinesDir() == "" {
		return ""
	}
	naming, _ := machineNames.Load().(machineNaming)
	return naming.displayName(naming.local)
}

// displayName returns the alias of name, or name when it has none.
func (n machineNaming) displayName(name string) string {
	if alias := n.aliases[name]; alias != "" {
		return alias
	}
	return name
}

// projectsRoot is one directory laid out like ~/.claude/projects, together
// with the name of the machine its files come from ("" in single-machine
// mode).
type projectsRoot struct {
	machine string
	dir     string
}

// projectsRoots returns the local projects directory followed by one entry
// per machine under the machines directory, in directory order. In
// multi-machine mode each root carries its machine name, after aliases: the
// local name for the local root, the subdirectory name for the others. Hidden
// entries and machines without a projects/ directory are skipped. An
// unreadable machines directory yields only the local root.
func projectsRoots(claudeDir string) []projectsRoot {
	local := filepath.Join(claudeDir, "projects")
	mdir := getMachinesDir()
	if mdir == "" {
		return []projectsRoot{{machine: "", dir: local}}
	}

	naming, _ := machineNames.Load().(machineNaming)
	roots := []projectsRoot{{machine: naming.displayName(naming.local), dir: local}}
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
		roots = append(roots, projectsRoot{machine: naming.displayName(name), dir: dir})
	}
	return roots
}
