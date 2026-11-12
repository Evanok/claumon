package service

import "errors"

var errAgentDarwin = errors.New("automatic start of the agent is not supported on macOS yet: run 'claumon agent' from a LaunchAgent")

// InstallAgent is not supported on macOS yet.
func InstallAgent(string, bool) (string, error) { return "", errAgentDarwin }

// UninstallAgent is not supported on macOS yet.
func UninstallAgent() error { return errAgentDarwin }

// AgentInstalled reports "" on macOS, where the agent has no automatic start.
func AgentInstalled() string { return "" }
