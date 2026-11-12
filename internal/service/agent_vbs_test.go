package service

import "testing"

func TestAgentVBS(t *testing.T) {
	got := agentVBS(`C:\Users\A\bin\claumon.exe`, `C:\Users\A\.claumon\agent.log`)
	want := `CreateObject("WScript.Shell").Run """C:\Users\A\bin\claumon.exe"" agent --log-file ""C:\Users\A\.claumon\agent.log""", 0, False` + "\r\n"
	if got != want {
		t.Errorf("agentVBS =\n%s\nwant\n%s", got, want)
	}
}
