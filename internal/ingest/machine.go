package ingest

import (
	"os"
	"regexp"
	"strings"
)

// DefaultMachineName names the machine an agent runs on: its hostname, with
// a "wsl-" prefix under WSL, where the Linux distribution shares the Windows
// host's hostname and would otherwise collide with it. Characters a machine
// name cannot hold are replaced by '-'.
func DefaultMachineName() string {
	host, _ := os.Hostname()
	osrelease, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return machineName(host, os.Getenv("WSL_DISTRO_NAME") != "" || isWSLKernel(string(osrelease)))
}

func isWSLKernel(osrelease string) bool {
	return strings.Contains(strings.ToLower(osrelease), "microsoft")
}

var invalidMachineChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func machineName(host string, wsl bool) string {
	name := invalidMachineChars.ReplaceAllString(host, "-")
	name = strings.TrimLeft(name, "._-")
	if name == "" {
		name = "machine"
	}
	if wsl {
		name = "wsl-" + name
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
