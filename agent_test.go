package main

import (
	"io"
	"testing"
	"time"
)

func TestParseAgentOptions(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent = AgentConfig{ServerURL: "https://cfg.example", Token: "cfg-token", Machine: "cfg-machine", IntervalSecs: 90}

	tests := []struct {
		name    string
		cfg     Config
		args    []string
		want    agentOptions
		wantErr bool
	}{
		{
			name: "config file",
			cfg:  cfg,
			want: agentOptions{serverURL: "https://cfg.example", token: "cfg-token", machine: "cfg-machine", interval: 90 * time.Second},
		},
		{
			name: "flags win",
			cfg:  cfg,
			args: []string{"--server", "https://flag.example", "--token", "t", "--machine", "m", "--interval", "5s", "--once", "--claude-dir", "/data/claude"},
			want: agentOptions{serverURL: "https://flag.example", token: "t", machine: "m", interval: 5 * time.Second, once: true},
		},
		{name: "missing token", cfg: defaultConfig(), args: []string{"--server", "https://x"}, wantErr: true},
		{name: "invalid machine", cfg: cfg, args: []string{"--machine", "../x"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAgentOptions(tt.cfg, tt.args, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.serverURL != tt.want.serverURL || got.token != tt.want.token || got.machine != tt.want.machine ||
				got.interval != tt.want.interval || got.once != tt.want.once {
				t.Errorf("options = %+v, want %+v", got, tt.want)
			}
			wantDir := tt.cfg.ClaudeDir
			if tt.name == "flags win" {
				wantDir = "/data/claude"
			}
			if got.claudeDir != wantDir || got.statePath == "" {
				t.Errorf("claude dir / state = %q / %q", got.claudeDir, got.statePath)
			}
		})
	}

	// Without a machine anywhere, the default name is used.
	noMachine := cfg
	noMachine.Agent.Machine = ""
	got, err := parseAgentOptions(noMachine, nil, io.Discard)
	if err != nil || got.machine == "" {
		t.Errorf("default machine: %q, %v", got.machine, err)
	}
	if got.interval != 90*time.Second {
		t.Errorf("interval = %s", got.interval)
	}
	noInterval := cfg
	noInterval.Agent.IntervalSecs = 0
	if got, _ := parseAgentOptions(noInterval, nil, io.Discard); got.interval != defaultAgentInterval {
		t.Errorf("default interval = %s", got.interval)
	}
}
