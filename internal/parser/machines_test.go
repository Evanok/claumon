package parser

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// minimalSession is a one-exchange session: one user line and one assistant
// line with usage, enough for parseSessionFiles to keep it.
const minimalSession = `{"type":"user","timestamp":"2026-11-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-11-01T10:00:05Z","requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":5}}}
`

// writeSession creates root/<project>/<id>.jsonl with minimalSession.
func writeSession(t *testing.T, root, project, id string) {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(minimalSession), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setMachinesDirForTest sets the machines directory and restores
// single-machine mode when the test ends.
func setMachinesDirForTest(t *testing.T, dir string) {
	t.Helper()
	SetMachinesDir(dir)
	t.Cleanup(func() { SetMachinesDir("") })
}

func TestEnumerateSessionFilesMachines(t *testing.T) {
	tests := []struct {
		name string
		// setup builds the tree under base and returns (claudeDir, machinesDir).
		setup   func(t *testing.T, base string) (string, string)
		want    []string // "machine|session id", any order
		wantErr bool
	}{
		{
			name: "local only, no machines dir",
			setup: func(t *testing.T, base string) (string, string) {
				claude := filepath.Join(base, "claude")
				writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "s1")
				return claude, ""
			},
			want: []string{"|s1"},
		},
		{
			name: "local plus two machines",
			setup: func(t *testing.T, base string) (string, string) {
				claude := filepath.Join(base, "claude")
				machines := filepath.Join(base, "machines")
				writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "s1")
				writeSession(t, filepath.Join(machines, "office", "projects"), "-home-a-proj", "s2")
				writeSession(t, filepath.Join(machines, "home", "projects"), "-home-b-other", "s3")
				return claude, machines
			},
			want: []string{"|s1", "home|s3", "office|s2"},
		},
		{
			name: "machine without projects dir and hidden dir are skipped",
			setup: func(t *testing.T, base string) (string, string) {
				claude := filepath.Join(base, "claude")
				machines := filepath.Join(base, "machines")
				writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "s1")
				if err := os.MkdirAll(filepath.Join(machines, "empty"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeSession(t, filepath.Join(machines, ".tmp", "projects"), "-home-a-proj", "hidden")
				if err := os.WriteFile(filepath.Join(machines, "stray.txt"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				return claude, machines
			},
			want: []string{"|s1"},
		},
		{
			name: "missing local projects dir is tolerated with machines",
			setup: func(t *testing.T, base string) (string, string) {
				machines := filepath.Join(base, "machines")
				writeSession(t, filepath.Join(machines, "office", "projects"), "-home-a-proj", "s2")
				return filepath.Join(base, "no-claude"), machines
			},
			want: []string{"office|s2"},
		},
		{
			name: "missing local projects dir is an error without machines",
			setup: func(t *testing.T, base string) (string, string) {
				return filepath.Join(base, "no-claude"), ""
			},
			wantErr: true,
		},
		{
			name: "nothing readable is an error",
			setup: func(t *testing.T, base string) (string, string) {
				return filepath.Join(base, "no-claude"), filepath.Join(base, "no-machines")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeDir, mdir := tt.setup(t, t.TempDir())
			setMachinesDirForTest(t, mdir)

			files, err := enumerateSessionFiles(claudeDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %d files", len(files))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got []string
			for _, f := range files {
				got = append(got, f.machine+"|"+strings.TrimSuffix(filepath.Base(f.path), ".jsonl"))
			}
			sort.Strings(got)
			sort.Strings(tt.want)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("files = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoverSessionsTagsMachine(t *testing.T) {
	base := t.TempDir()
	claude := filepath.Join(base, "claude")
	machines := filepath.Join(base, "machines")
	writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "local1")
	writeSession(t, filepath.Join(machines, "office", "projects"), "-home-a-proj", "remote1")
	setMachinesDirForTest(t, machines)

	sessions, err := DiscoverSessions(claude)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range sessions {
		got[s.ID] = s.Machine
	}
	want := map[string]string{"local1": "", "remote1": "office"}
	if len(got) != len(want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	for id, m := range want {
		if machine, ok := got[id]; !ok || machine != m {
			t.Errorf("session %s machine = %q (found %v), want %q", id, machine, ok, m)
		}
	}
}

// setMachineNamesForTest sets the machine names and clears them when the
// test ends.
func setMachineNamesForTest(t *testing.T, local string, aliases map[string]string) {
	t.Helper()
	SetMachineNames(local, aliases)
	t.Cleanup(func() { SetMachineNames("", nil) })
}

func TestMachineNamesAndAliases(t *testing.T) {
	base := t.TempDir()
	claude := filepath.Join(base, "claude")
	machines := filepath.Join(base, "machines")
	writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "local1")
	writeSession(t, filepath.Join(machines, "DESKTOP-7QK2", "projects"), "-home-a-proj", "win1")
	writeSession(t, filepath.Join(machines, "laptop", "projects"), "-home-a-proj", "lap1")
	// The same machine synced under two directory names, merged by aliases.
	writeSession(t, filepath.Join(machines, "wsl-old", "projects"), "-home-a-proj", "shared")
	writeSession(t, filepath.Join(machines, "wsl-new", "projects"), "-home-a-proj", "shared")
	setMachinesDirForTest(t, machines)

	aliases := map[string]string{
		"build-host-01": "office",
		"DESKTOP-7QK2":  "home-windows",
		"wsl-old":       "home-wsl",
		"wsl-new":       "home-wsl",
		"unused":        "",
	}
	setMachineNamesForTest(t, "build-host-01", aliases)
	aliases["laptop"] = "changed after the call" // must not leak in

	sessions, err := DiscoverSessions(claude)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range sessions {
		got[s.ID] = s.Machine + "|" + strings.Join(s.Machines, ",")
	}
	want := map[string]string{
		"local1": "office|office",
		"win1":   "home-windows|home-windows",
		"lap1":   "laptop|laptop",
		"shared": "home-wsl|home-wsl",
	}
	if len(got) != len(want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("session %s machine|machines = %q, want %q", id, got[id], w)
		}
	}

	// Without a machines directory, sessions carry no machine name even when
	// names are set.
	SetMachinesDir("")
	sessions, err = DiscoverSessions(claude)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.Machine != "" || len(s.Machines) != 0 {
			t.Errorf("single-machine mode: session %s machine=%q machines=%v", s.ID, s.Machine, s.Machines)
		}
	}
}

func TestFindSessionFileMachines(t *testing.T) {
	base := t.TempDir()
	claude := filepath.Join(base, "claude")
	machines := filepath.Join(base, "machines")
	writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "local1")
	writeSession(t, filepath.Join(machines, "office", "projects"), "-home-a-proj", "remote1")
	writeSession(t, filepath.Join(machines, "office", "projects"), "-home-a-proj", "both")
	writeSession(t, filepath.Join(claude, "projects"), "-home-a-proj", "both")

	tests := []struct {
		name     string
		mdir     string
		id       string
		wantPath string // "" means not found
	}{
		{"local without machines", "", "local1", filepath.Join(claude, "projects", "-home-a-proj", "local1.jsonl")},
		{"remote hidden without machines", "", "remote1", ""},
		{"remote with machines", machines, "remote1", filepath.Join(machines, "office", "projects", "-home-a-proj", "remote1.jsonl")},
		// Identical copies tie on activity and size; the smaller path wins.
		{"identical copies resolve by path", machines, "both", filepath.Join(claude, "projects", "-home-a-proj", "both.jsonl")},
		{"unknown id", machines, "nope", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setMachinesDirForTest(t, tt.mdir)
			if got := FindSessionFile(claude, tt.id); got != tt.wantPath {
				t.Errorf("FindSessionFile(%q) = %q, want %q", tt.id, got, tt.wantPath)
			}
		})
	}
}

func TestDiscoverMachineDailyAggregates(t *testing.T) {
	base := t.TempDir()
	older := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	writeLedgerFiles(t, base, []ledgerFile{
		// s1 ran locally; the office copy repeats it and adds a next-day
		// exchange. The repeated records count for the local machine (its
		// copy was modified first), the new ones for office.
		{"claude/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1}, older},
		{"machines/office/projects/-p/s1.jsonl", []string{ledgerUser1, ledgerResp1, ledgerUser3, ledgerResp3}, newer},
		{"machines/office/projects/-p/s2.jsonl", []string{ledgerUser2, ledgerResp2}, newer},
	})
	claude := filepath.Join(base, "claude")

	setMachinesDirForTest(t, "")
	if got, err := DiscoverMachineDailyAggregates(claude); err != nil || got != nil {
		t.Fatalf("single-machine mode: got %v, %v; want nil, nil", got, err)
	}

	setMachinesDirForTest(t, filepath.Join(base, "machines"))
	setMachineNamesForTest(t, "laptop", nil)
	got, err := DiscoverMachineDailyAggregates(claude)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ input, messages, sessions int }
	wants := map[string]map[string]want{
		day(20): {"laptop": {100, 2, 1}, "office": {20, 2, 1}},
		day(21): {"office": {3, 2, 1}},
	}
	for date, machines := range wants {
		if len(got[date]) != len(machines) {
			t.Errorf("%s: machines = %v, want %v", date, got[date], machines)
		}
		for m, w := range machines {
			a := got[date][m]
			if a.InputTokens != w.input || a.MessageCount != w.messages || a.SessionCount != w.sessions {
				t.Errorf("%s/%s: input/messages/sessions = %d/%d/%d, want %d/%d/%d",
					date, m, a.InputTokens, a.MessageCount, a.SessionCount, w.input, w.messages, w.sessions)
			}
		}
	}

	// The machines of a day add up to the day's total.
	daily, err := DiscoverDailyAggregates(claude)
	if err != nil {
		t.Fatal(err)
	}
	for date, machines := range got {
		var input, messages int
		var cost float64
		for _, a := range machines {
			input += a.InputTokens
			messages += a.MessageCount
			cost += a.CostUSD
		}
		if d := daily[date]; d.InputTokens != input || d.MessageCount != messages || math.Abs(d.CostUSD-cost) > 0.0002 {
			t.Errorf("%s: machines sum input/messages/cost = %d/%d/%v, day total %d/%d/%v",
				date, input, messages, cost, d.InputTokens, d.MessageCount, d.CostUSD)
		}
	}
}

func TestDiscoverMachineStorage(t *testing.T) {
	setMachinesDirForTest(t, "")
	if got, err := DiscoverMachineStorage(); err != nil || got != nil {
		t.Fatalf("single-machine mode: got %v, %v; want nil, nil", got, err)
	}

	machines := filepath.Join(t.TempDir(), "machines")
	write := func(rel string, size int) {
		t.Helper()
		path := filepath.Join(machines, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("office/projects/-p/s1.jsonl", 100)
	write("office/projects/-p/s1/subagents/a.jsonl", 20)
	write("office/settings.json", 3) // not read by claumon, still stored
	write("wsl-old/projects/-p/s2.jsonl", 40)
	write("wsl-new/projects/-p/s3.jsonl", 50)
	write(".tmp/projects/-p/s4.jsonl", 1000) // hidden: skipped
	write("stray.txt", 7)                    // not a machine directory
	if err := os.MkdirAll(filepath.Join(machines, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	setMachinesDirForTest(t, machines)
	setMachineNamesForTest(t, "laptop", map[string]string{"wsl-old": "home", "wsl-new": "home"})

	got, err := DiscoverMachineStorage()
	if err != nil {
		t.Fatal(err)
	}
	want := []MachineStorage{
		{Machine: "empty", Bytes: 0, Files: 0},
		{Machine: "home", Bytes: 90, Files: 2},
		{Machine: "office", Bytes: 123, Files: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("machine %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
