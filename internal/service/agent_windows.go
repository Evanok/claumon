package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// On Windows the agent starts at logon from a VBScript in the Startup
// folder, hidden, like the dashboard service, logging to
// %USERPROFILE%\.claumon\agent.log.

func agentVBSPath() string {
	return filepath.Join(startupDir(), "claumon-agent.vbs")
}

// InstallAgent registers the agent to start at logon and starts it.
func InstallAgent(execPath string, _ bool) (string, error) {
	clearMarkOfTheWeb(execPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	logPath := filepath.Join(home, ".claumon", "agent.log")
	if err := os.WriteFile(agentVBSPath(), []byte(agentVBS(execPath, logPath)), 0o644); err != nil {
		return "", fmt.Errorf("writing startup script: %w", err)
	}
	if err := exec.Command("wscript.exe", agentVBSPath()).Start(); err != nil {
		return "", fmt.Errorf("starting the agent: %w", err)
	}
	return "startup script " + agentVBSPath() + " (logs: " + logPath + ")", nil
}

// UninstallAgent removes the startup script. A running agent stops at the
// next logoff, or with Task Manager.
func UninstallAgent() error {
	if err := os.Remove(agentVBSPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing startup script: %w", err)
	}
	return nil
}

// AgentInstalled describes how the agent starts automatically, or returns
// "" when it does not.
func AgentInstalled() string {
	if _, err := os.Stat(agentVBSPath()); err == nil {
		return "startup script " + agentVBSPath()
	}
	return ""
}
