package service

import (
	"fmt"
	"strings"
)

// agentVBS is the Windows startup script running the agent hidden (window
// style 0). It lives outside agent_windows.go so its test runs everywhere.
func agentVBS(execPath, logPath string) string {
	cmd := fmt.Sprintf(`"%s" agent --log-file "%s"`, execPath, logPath)
	// A VBScript string literal escapes a quote by doubling it.
	return `CreateObject("WScript.Shell").Run "` + strings.ReplaceAll(cmd, `"`, `""`) + "\", 0, False\r\n"
}
