package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The agent (`claumon agent`) runs as a systemd user unit when systemd is
// available. WSL distributions often run without systemd; there the agent
// is started from ~/.profile when a WSL shell opens, which is also the only
// time Claude Code can run in it. The agent's lock makes repeated starts
// harmless.

const linuxAgentServiceName = "claumon-agent"

// Markers delimiting the block InstallAgent adds to ~/.profile.
const (
	profileBlockStart = "# >>> claumon agent >>>"
	profileBlockEnd   = "# <<< claumon agent <<<"
)

func agentUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", linuxAgentServiceName+".service"), nil
}

func agentUnitContent(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=claumon agent — pushes Claude Code sessions to a claumon server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="%s" agent
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
`, execPath)
}

// profileBlock is the ~/.profile snippet starting the agent in the
// background, logging to ~/.claumon/agent.log.
func profileBlock(execPath string) string {
	q := shellQuote(execPath)
	return profileBlockStart + `
# Added by 'claumon agent install': pushes this machine's Claude Code
# sessions to a claumon server. Remove with 'claumon agent uninstall'.
if [ -x ` + q + ` ]; then
  ( ` + q + ` agent --log-file "$HOME/.claumon/agent.log" >/dev/null 2>&1 & )
fi
` + profileBlockEnd + "\n"
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// withProfileBlock returns content with block, replacing a previous block.
func withProfileBlock(content, block string) string {
	content = withoutProfileBlock(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	return content + block
}

// withoutProfileBlock returns content without the agent block, if any.
func withoutProfileBlock(content string) string {
	start := strings.Index(content, profileBlockStart)
	if start < 0 {
		return content
	}
	end := strings.Index(content[start:], profileBlockEnd)
	if end < 0 {
		return content
	}
	end += start + len(profileBlockEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	before := strings.TrimRight(content[:start], "\n")
	if before != "" {
		before += "\n"
	}
	return before + content[end:]
}

func profilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".profile"), nil
}

// systemdUserAvailable reports whether systemd user units can be used.
func systemdUserAvailable() bool {
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

// InstallAgent registers the agent to start automatically and starts it
// when it can. It returns a description of what it set up.
func InstallAgent(execPath string, wsl bool) (string, error) {
	if systemdUserAvailable() {
		path, err := agentUnitPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("creating systemd user dir: %w", err)
		}
		if err := os.WriteFile(path, []byte(agentUnitContent(execPath)), 0o644); err != nil {
			return "", fmt.Errorf("writing unit file: %w", err)
		}
		for _, args := range [][]string{{"daemon-reload"}, {"enable", linuxAgentServiceName}, {"restart", linuxAgentServiceName}} {
			if err := exec.Command("systemctl", append([]string{"--user"}, args...)...).Run(); err != nil {
				return "", fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
			}
		}
		return "systemd user unit " + path + " (logs: journalctl --user -u " + linuxAgentServiceName + ")", nil
	}
	if !wsl {
		return "", errors.New("systemd user services are not available; run 'claumon agent' from your session startup instead")
	}

	path, err := profilePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(withProfileBlock(string(data), profileBlock(execPath))), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	// Start it now, detached, like the next shell will.
	logPath := filepath.Join(filepath.Dir(path), ".claumon", "agent.log")
	cmd := exec.Command(execPath, "agent", "--log-file", logPath)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting the agent: %w", err)
	}
	cmd.Process.Release()
	return "start from " + path + " when a WSL shell opens (logs: " + logPath + ")", nil
}

// UninstallAgent stops the agent and removes its automatic start.
func UninstallAgent() error {
	if path, err := agentUnitPath(); err == nil {
		if _, err := os.Stat(path); err == nil {
			exec.Command("systemctl", "--user", "disable", "--now", linuxAgentServiceName).Run()
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing unit file: %w", err)
			}
			exec.Command("systemctl", "--user", "daemon-reload").Run()
		}
	}
	path, err := profilePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(data), profileBlockStart) {
		if err := os.WriteFile(path, []byte(withoutProfileBlock(string(data))), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	stopRunningAgents()
	return nil
}

// stopRunningAgents sends SIGTERM to the running agents, except this
// process (itself a "claumon agent uninstall").
func stopRunningAgents() {
	out, err := exec.Command("pgrep", "-f", "claumon agent").Output()
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid == os.Getpid() {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			p.Signal(syscall.SIGTERM)
		}
	}
}

// AgentInstalled describes how the agent starts automatically, or returns
// "" when it does not.
func AgentInstalled() string {
	if path, err := agentUnitPath(); err == nil {
		if _, err := os.Stat(path); err == nil {
			return "systemd user unit " + linuxAgentServiceName
		}
	}
	if path, err := profilePath(); err == nil {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), profileBlockStart) {
			return "started from " + path
		}
	}
	return ""
}
