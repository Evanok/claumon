package ingest

import (
	"strings"
	"testing"
)

func TestMachineName(t *testing.T) {
	tests := []struct {
		host string
		wsl  bool
		want string
	}{
		{"soundskrit-XPS-8940", false, "soundskrit-XPS-8940"},
		{"DESKTOP-OM7HLL9", true, "wsl-DESKTOP-OM7HLL9"},
		{"Arthur's MacBook Pro", false, "Arthur-s-MacBook-Pro"},
		{"", false, "machine"},
		{".local", false, "local"},
		{strings.Repeat("h", 80), true, "wsl-" + strings.Repeat("h", 60)},
	}
	for _, tt := range tests {
		got := machineName(tt.host, tt.wsl)
		if got != tt.want {
			t.Errorf("machineName(%q, %v) = %q, want %q", tt.host, tt.wsl, got, tt.want)
		}
		if !ValidMachineName(got) {
			t.Errorf("machineName(%q, %v) = %q is not a valid machine name", tt.host, tt.wsl, got)
		}
	}
	for release, want := range map[string]bool{
		"5.15.167.4-microsoft-standard-WSL2": true,
		"4.4.0-19041-Microsoft":              true,
		"6.17.0-29-generic":                  false,
	} {
		if got := isWSLKernel(release); got != want {
			t.Errorf("isWSLKernel(%q) = %v, want %v", release, got, want)
		}
	}
}
