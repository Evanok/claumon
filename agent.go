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

	"github.com/fabioconcina/claumon/internal/api"
	"github.com/fabioconcina/claumon/internal/auth"
	"github.com/fabioconcina/claumon/internal/ingest"
	"github.com/fabioconcina/claumon/internal/service"
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
	noUsage                   bool
	statePath                 string
	claudeDir                 string
	logFile                   string
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
	noUsage := fs.Bool("no-usage", false, "do not fetch the usage gauges for the server")
	statePath := fs.String("state", filepath.Join(filepath.Dir(cfg.DBPath), "agent-state.json"), "file recording what was sent")
	claudeDir := fs.String("claude-dir", cfg.ClaudeDir, "Claude directory whose projects are pushed")
	logFile := fs.String("log-file", "", "append logs to this file instead of stderr")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: claumon agent [flags]")
		fmt.Fprintln(stderr, "       claumon agent install|uninstall|status")
		fmt.Fprintln(stderr, "Pushes this machine's Claude Code session files to a claumon server (see MULTI_MACHINE.md).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return agentOptions{}, err
	}

	o := agentOptions{
		serverURL: *server, token: *token, machine: *machine,
		interval: *interval, once: *once, noUsage: *noUsage, statePath: *statePath, claudeDir: expandHome(*claudeDir, homeDir()),
		logFile: expandHome(*logFile, homeDir()),
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
	if len(os.Args) > 2 {
		switch os.Args[2] {
		case "install", "uninstall", "status":
			runAgentService(cfg, os.Args[2])
			return
		}
	}
	o, err := parseAgentOptions(cfg, os.Args[2:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "claumon agent: %v\n", err)
		os.Exit(2)
	}

	if o.logFile != "" {
		f, err := openAgentLog(o.logFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "claumon agent: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	// One agent per machine: a second one (another shell, a restart racing
	// the old process) exits quietly.
	unlock, err := lockAgent(filepath.Join(filepath.Dir(o.statePath), "agent.lock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "claumon agent: %v\n", err)
		return
	}
	defer unlock()

	a := &ingest.Agent{
		ClaudeDir: o.claudeDir,
		ServerURL: o.serverURL,
		Token:     o.token,
		Machine:   o.machine,
		// Leave out what claumon never reads (api-request lines, images,
		// duplicated tool results): about 80% of the size.
		Filter: ingest.SlimLine,
		Client: &http.Client{Timeout: 2 * time.Minute},
		State:  ingest.LoadState(o.statePath),
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
	if !o.noUsage {
		go a.RunUsage(ctx, usageFetcher(cfg, o.claudeDir), time.Minute, log.Printf)
	}
	a.Run(ctx, o.interval, log.Printf)
}

// usageFetcher fetches the usage gauges with this machine's Claude
// credentials, which Claude Code keeps fresh while it is used here.
func usageFetcher(cfg Config, claudeDir string) ingest.UsageFetcher {
	provider := auth.NewProvider(claudeDir, cfg.CredentialsPath)
	client := api.NewClient(provider)
	return func(ctx context.Context) ([]byte, string, string, error) {
		if !provider.HasToken() {
			if provider.Reload() != nil || !provider.HasToken() {
				return nil, "", "", ingest.ErrNoCredentials
			}
		}
		usage, err := client.FetchUsage(ctx)
		var authErr *api.AuthError
		if errors.As(err, &authErr) {
			return nil, "", "", fmt.Errorf("%w (%s)", ingest.ErrNoCredentials, authErr.Message)
		}
		if err != nil {
			return nil, "", "", err
		}
		var sub, tier string
		if creds := provider.Credentials(); creds != nil {
			sub, tier = creds.SubscriptionType, creds.RateLimitTier
		}
		return usage.Raw, sub, tier, nil
	}
}

// homeDir returns the user's home directory, or "" when unknown.
func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// maxAgentLogBytes is the size above which the agent log is rotated at
// startup, keeping one previous file.
const maxAgentLogBytes = 5 << 20

// openAgentLog opens the agent log for appending, after moving a log larger
// than maxAgentLogBytes to <path>.old.
func openAgentLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxAgentLogBytes {
		os.Rename(path, path+".old")
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

// runAgentService handles claumon agent install, uninstall and status.
func runAgentService(cfg Config, action string) {
	switch action {
	case "install":
		// Refuse to install an agent that would fail at every start.
		o, err := parseAgentOptions(cfg, nil, io.Discard)
		if err != nil {
			fmt.Fprintf(os.Stderr, "claumon agent install: %v\n", err)
			os.Exit(1)
		}
		execPath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "claumon agent install: resolving the executable: %v\n", err)
			os.Exit(1)
		}
		how, err := service.InstallAgent(execPath, ingest.IsWSL())
		if err != nil {
			fmt.Fprintf(os.Stderr, "claumon agent install: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("claumon agent installed: pushes this machine as %q to %s\n", o.machine, o.serverURL)
		fmt.Printf("Starts with: %s\n", how)
		fmt.Println("To remove:   claumon agent uninstall")
	case "uninstall":
		if err := service.UninstallAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "claumon agent uninstall: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("claumon agent removed from startup.")
	case "status":
		how := service.AgentInstalled()
		if how == "" {
			fmt.Println("Agent: not installed")
			return
		}
		fmt.Printf("Agent: installed (%s)\n", how)
	}
}
