package parser

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MachineStorage is the disk space used by one machine's directory under the
// machines directory.
type MachineStorage struct {
	Machine string `json:"machine"` // display name, after aliases
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
}

// DiscoverMachineStorage returns the disk space used under each machine
// directory of the machines directory, sorted by machine name. Every regular
// file counts, including those claumon does not read, since they take space
// all the same. Directories whose names share an alias are reported as one
// machine. It returns nil in single-machine mode.
func DiscoverMachineStorage() ([]MachineStorage, error) {
	mdir := getMachinesDir()
	if mdir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(mdir)
	if err != nil {
		return nil, err
	}

	naming, _ := machineNames.Load().(machineNaming)
	byName := map[string]*MachineStorage{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := naming.displayName(e.Name())
		usage := byName[name]
		if usage == nil {
			usage = &MachineStorage{Machine: name}
			byName[name] = usage
		}
		// Files vanishing during the walk (a copy in progress) are skipped.
		filepath.WalkDir(filepath.Join(mdir, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				usage.Bytes += info.Size()
				usage.Files++
			}
			return nil
		})
	}

	out := make([]MachineStorage, 0, len(byName))
	for _, u := range byName {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Machine < out[j].Machine })
	return out, nil
}
