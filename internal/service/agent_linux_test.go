package service

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProfileBlock(t *testing.T) {
	block := profileBlock("/home/a/bin/claumon")
	tests := []struct {
		name, in, want string
	}{
		{"empty profile", "", block},
		{"appended after content", "export A=1\n", "export A=1\n\n" + block},
		{"content without final newline", "export A=1", "export A=1\n\n" + block},
		{"replaces an older block", "export A=1\n\n" + profileBlock("/old/claumon") + "export B=2\n", "export A=1\nexport B=2\n\n" + block},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withProfileBlock(tt.in, block)
			if got != tt.want {
				t.Errorf("withProfileBlock:\n%s\nwant:\n%s", got, tt.want)
			}
			if strings.Count(got, profileBlockStart) != 1 {
				t.Errorf("%d blocks", strings.Count(got, profileBlockStart))
			}
		})
	}

	for in, want := range map[string]string{
		"export A=1\n\n" + block:                  "export A=1\n",
		"export A=1\n\n" + block + "export B=2\n": "export A=1\nexport B=2\n",
		block:        "",
		"no block\n": "no block\n",
	} {
		if got := withoutProfileBlock(in); got != want {
			t.Errorf("withoutProfileBlock(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProfileBlockIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// A path with a quote must stay one word; the block must parse.
	block := profileBlock("/home/o'brien/bin/claumon")
	if out, err := exec.Command(sh, "-n", "-c", block).CombinedOutput(); err != nil {
		t.Fatalf("block does not parse: %v\n%s\n%s", err, out, block)
	}
	if !strings.Contains(block, `'/home/o'\''brien/bin/claumon'`) {
		t.Errorf("path not quoted:\n%s", block)
	}
}

func TestAgentUnitContent(t *testing.T) {
	unit := agentUnitContent("/opt/claumon")
	for _, want := range []string{`ExecStart="/opt/claumon" agent`, "Restart=always", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
}
