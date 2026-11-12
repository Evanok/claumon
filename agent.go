package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fabioconcina/claumon/internal/ingest"
)

// AgentConfig configures `claumon agent`, which pushes this machine's
// session files to a claumon server in multi-machine mode.
type AgentConfig struct {
	ServerURL    string `json:"server_url,omitempty"`
	Token        string `json:"token,omitempty"`
	Machine      string `json:"machine,omitempty"` // default: hostname, wsl-<hostname> under WSL
	IntervalSecs int    `json:"interval_seconds,omitempty"`
}

const defaultAgentInterval = 30 * time.Second

// agentOptions are the resolved settings of an agent run.
type agentOptions struct {
	serverURL, token, machine string
	interval                  time.Duration
	once                      bool
	statePath                 string
	claudeDir                 string
}

// parseAgentOptions resolves the agent settings: command-line flags over the
// "agent" section of the config file over defaults.
func parseAgentOptions(cfg Config, args []string, stderr io.Writer) (agentOptions, error) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", cfg.Agent.ServerURL, "URL of the claumon server")
	token := fs.String("token", cfg.Agent.Token, "ingest token, one of the server's ingest_tokens")
	machine := fs.String("machine", cfg.Agent.Machine, "machine name (default: hostname, wsl-<hostname> under WSL)")
	interval := fs.Duration("interval", 0, "time between two syncs (default 30s)")
	once := fs.Bool("once", false, "sync once and exit")
	statePath := fs.String("state", filepath.Join(filepath.Dir(cfg.DBPath), "agent-state.json"), "file recording what was sent")
	claudeDir := fs.String("claude-dir", cfg.ClaudeDir, "Claude directory whose projects are pushed")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: claumon agent [flags]")
		fmt.Fprintln(stderr, "Pushes this machine's Claude Code session files to a claumon server (see MULTI_MACHINE.md).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return agentOptions{}, err
	}

	o := agentOptions{
		serverURL: *server, token: *token, machine: *machine,
		interval: *interval, once: *once, statePath: *statePath, claudeDir: expandHome(*claudeDir, homeDir()),
	}
	if o.interval <= 0 && cfg.Agent.IntervalSecs > 0 {
		o.interval = time.Duration(cfg.Agent.IntervalSecs) * time.Second
	}
	if o.interval <= 0 {
		o.interval = defaultAgentInterval
	}
	if o.machine == "" {
		o.machine = ingest.DefaultMachineName()
	}
	switch {
	case o.serverURL == "" || o.token == "":
		return o, errors.New("the server URL and token are required (flags --server and --token, or the agent section of config.json)")
	case !ingest.ValidMachineName(o.machine):
		return o, fmt.Errorf("invalid machine name %q: use letters, digits, '.', '_' or '-'", o.machine)
	}
	return o, nil
}

func runAgent() {
	cfg := loadConfig()
	o, err := parseAgentOptions(cfg, os.Args[2:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "claumon agent: %v\n", err)
		os.Exit(2)
	}

	a := &ingest.Agent{
		ClaudeDir: o.claudeDir,
		ServerURL: o.serverURL,
		Token:     o.token,
		Machine:   o.machine,
		Client:    &http.Client{Timeout: 2 * time.Minute},
		State:     ingest.LoadState(o.statePath),
	}
	log.Printf("[agent] Pushing %s as machine %q to %s", filepath.Join(o.claudeDir, "projects"), o.machine, o.serverURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if o.once {
		stats, err := a.Sync(ctx)
		if saveErr := a.State.Save(); saveErr != nil {
			log.Printf("[agent] Failed to save state: %v", saveErr)
		}
		log.Printf("[agent] Sent %d files: %.1f MB of transcripts, %.1f MB after filtering",
			stats.Files, float64(stats.SourceSent)/(1<<20), float64(stats.BytesSent)/(1<<20))
		if err != nil {
			log.Fatalf("[agent] Sync failed: %v", err)
		}
		return
	}
	a.Run(ctx, o.interval, log.Printf)
}

// homeDir returns the user's home directory, or "" when unknown.
func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}
