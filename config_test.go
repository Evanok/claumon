package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := filepath.Join("/home", "alice")
	tests := []struct {
		name string
		path string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde prefix", "~/.claude", filepath.Join(home, ".claude")},
		{"nested", "~/a/b/c.json", filepath.Join(home, "a", "b", "c.json")},
		{"absolute", "/var/lib/claumon", "/var/lib/claumon"},
		{"relative", "data/usage.db", "data/usage.db"},
		{"empty", "", ""},
		{"other user", "~bob/x", "~bob/x"},
		{"tilde not leading", "a/~/b", "a/~/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandHome(tt.path, home); got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestExpandHomeWithoutHome(t *testing.T) {
	if got := expandHome("~/.claude", ""); got != "~/.claude" {
		t.Errorf("expandHome with empty home = %q, want the path unchanged", got)
	}
}

func TestLoadConfigExpandsTildePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
		"claude_dir": "~/claude-sync",
		"credentials_path": "~/claude-sync/.credentials.json",
		"db_path": "~/.claumon-test/usage.db",
		"machines_dir": "~/claumon/machines"
	}`
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfigFrom(configPath)

	if want := filepath.Join(home, "claude-sync"); cfg.ClaudeDir != want {
		t.Errorf("ClaudeDir = %q, want %q", cfg.ClaudeDir, want)
	}
	if want := filepath.Join(home, "claude-sync", ".credentials.json"); cfg.CredentialsPath != want {
		t.Errorf("CredentialsPath = %q, want %q", cfg.CredentialsPath, want)
	}
	if want := filepath.Join(home, ".claumon-test", "usage.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if want := filepath.Join(home, "claumon", "machines"); cfg.MachinesDir != want {
		t.Errorf("MachinesDir = %q, want %q", cfg.MachinesDir, want)
	}
}

func TestLoadConfigMissingFileReturnsDefaults(t *testing.T) {
	cfg := loadConfigFrom(filepath.Join(t.TempDir(), "missing.json"))
	if cfg.Port != defaultConfig().Port {
		t.Errorf("Port = %d, want default %d", cfg.Port, defaultConfig().Port)
	}
	if cfg.MachinesDir != "" {
		t.Errorf("MachinesDir = %q, want empty (single-machine mode by default)", cfg.MachinesDir)
	}
}
