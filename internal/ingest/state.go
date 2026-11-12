package ingest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// State is the agent's record of the source offset sent per file, saved as
// JSON. It only saves round trips: the server's offsets are authoritative,
// so a lost or stale state is repaired by the server's 409 replies.
type State struct {
	path string

	mu      sync.Mutex
	offsets map[string]int64
	dirty   bool
}

// LoadState reads the state saved at path; a missing or unreadable file
// gives an empty state.
func LoadState(path string) *State {
	s := &State{path: path, offsets: map[string]int64{}}
	data, err := os.ReadFile(path)
	if err == nil {
		var saved struct {
			Offsets map[string]int64 `json:"offsets"`
		}
		if json.Unmarshal(data, &saved) == nil && saved.Offsets != nil {
			s.offsets = saved.Offsets
		}
	}
	return s
}

// Offset returns the source offset sent for a file.
func (s *State) Offset(file string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offsets[file]
}

// SetOffset records the source offset sent for a file.
func (s *State) SetOffset(file string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offsets[file] != offset {
		s.offsets[file] = offset
		s.dirty = true
	}
}

// Save writes the state when it changed, atomically (temporary file, then
// rename). A State without a path is kept in memory only.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty || s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(struct {
		Offsets map[string]int64 `json:"offsets"`
	}{s.offsets}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	s.dirty = false
	return nil
}
