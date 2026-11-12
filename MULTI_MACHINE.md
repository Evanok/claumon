# Multi-machine mode

By default claumon reads the session files of the machine it runs on
(`~/.claude/projects`). Multi-machine mode lets one claumon instance also read
session files collected from other machines, so a single dashboard shows the
usage of all of them. It is opt-in: without `machines_dir`, nothing changes.

## Directory layout

Point `machines_dir` at a directory holding one subdirectory per machine, each
laid out like a `~/.claude` directory:

```
<machines_dir>/
  office-laptop/
    projects/
      -home-alice-work-api/
        3f2c...e1.jsonl
  home-desktop/
    projects/
      -home-alice-work-api/
        3f2c...e1.jsonl
      -home-alice-notes/
        9a71...04.jsonl
```

- The machine name is the subdirectory name. Using each machine's hostname
  keeps names consistent with the local machine (see below).
- Only `<machine>/projects/<project>/*.jsonl` files are read, plus the
  subagent transcripts of each session
  (`<machine>/projects/<project>/<session>/subagents/*.jsonl`). Other files and
  directories are ignored, and so are entries whose name starts with `.`
  (handy for temporary files written during a copy).
- The local `~/.claude/projects` is still read, and its sessions are named
  after the hostname of the machine claumon runs on. A server that does not
  run Claude Code itself can have no local `projects` directory at all.

The files get there in one of two ways: `claumon agent` pushes them from
each machine (next section), or you copy them yourself, for example with
`rsync`:

```bash
rsync -a ~/.claude/projects/ server:claumon-machines/office-laptop/projects/
```

## Pushing sessions with `claumon agent`

`claumon agent` runs on each machine where Claude Code runs and pushes its
session files to the claumon server over HTTP(S). Every 30 minutes (the
agent's `interval_seconds`, or `--interval`) it sends
the complete lines appended to each session and subagent transcript since
the previous sync; the server appends them under
`<machines_dir>/<machine>/projects/...`.

On the server, set one or more tokens next to `machines_dir`. Any random
string works, for example the output of `openssl rand -hex 32`:

```json
{
  "machines_dir": "~/claumon-machines",
  "ingest_tokens": ["<token>"]
}
```

The `POST /api/ingest` route exists only when both are set. On each machine,
give the agent the server's URL and a token, in `~/.claumon/config.json`:

```json
{
  "agent": {
    "server_url": "https://claumon.example.com",
    "token": "<token>"
  }
}
```

then run `claumon agent` (or `claumon agent --once` to sync once and exit).
The flags `--server`, `--token`, `--machine`, `--interval` and `--claude-dir`
override the config file.

To start the agent automatically, run `claumon agent install` once the
`agent` section is set (`claumon agent status` and `claumon agent uninstall`
check and undo it):

- **Linux with systemd:** a user unit, `claumon-agent`
  (logs: `journalctl --user -u claumon-agent`).
- **WSL without systemd:** a block in `~/.profile` starts the agent in the
  background when a WSL shell opens, the only time Claude Code can run there.
  Only one agent runs at a time, so opening more shells is harmless. Logs go
  to `~/.claumon/agent.log`.
- **Windows:** a script in the Startup folder starts it hidden at logon.
  Logs go to `%USERPROFILE%\.claumon\agent.log`. `claumon service uninstall`
  and the dashboard's self-update stop every `claumon.exe`, the agent
  included; it starts again at the next logon.
- **macOS:** not supported yet; run `claumon agent` from a LaunchAgent.

- **Machine name.** The agent sends the machine's hostname, prefixed with
  `wsl-` under WSL, where the Linux distribution shares the Windows host's
  hostname. Set `machine` (or `--machine`) to choose another name, and use
  `machine_aliases` on the server for friendlier display names.
- **What is sent.** Only complete lines: a line still being written waits for
  the next sync. Lines claumon never reads are left out (the `api-request`
  lines, the base64 data of images, and the `toolUseResult` field, a copy of
  the tool results already in the messages): about 70% of the size on real
  transcripts, with identical totals and session views. The copy keeps the
  source file's modification time, which decides how a session found on
  several machines is attributed.
- **Resuming.** The server records how far it received each file, and the
  agent records it in `~/.claumon/agent-state.json`. An interrupted upload
  resumes where it stopped; when either side lost its records, the server's
  answer puts the agent back in step, and an agent checks every file with the
  server when it starts. Deleting a transcript on a machine never deletes the
  server's copy: the server is the archive.
- **Reverse proxy.** Agents authenticate with their token
  (`Authorization: Bearer`). A proxy that protects the dashboard with its own
  authentication must let `/api/ingest` through, and the proxy's hostname
  must be in `allowed_hosts`.

## Configuration

In `~/.claumon/config.json`:

```json
{
  "machines_dir": "~/claumon-machines"
}
```

or on the command line: `claumon --machines-dir ~/claumon-machines`. The flag
takes precedence over the config file. The directory must exist when claumon
starts; machines and projects added later are picked up live.

### Machine names

In multi-machine mode the session list gets a Machine column. A machine is
named after its hostname (local machine) or its subdirectory name (other
machines). `machine_aliases` replaces those names with friendlier ones:

```json
{
  "machines_dir": "~/claumon-machines",
  "machine_aliases": {
    "DESKTOP-7QK2LM": "home-windows",
    "build-host-01": "office"
  }
}
```

Names without an alias are shown as they are. Two names with the same alias
are shown as one machine. The local machine's hostname is logged at startup
(`[startup] multi-machine mode — ... local machine=<hostname>`).

## How sessions present on several machines are counted

The same session can exist in more than one file: copied to several machines,
or moved to another project directory (Claude Code then copies its history
into a new file, which also happens on a single machine). claumon counts each
record once:

- tokens and cost once per API response (message ID),
- messages once per JSONL line (line UUID),
- one row per session in the session list, built from all its copies. The
  descriptive fields (title, model, project, machine) come from the most
  complete copy, the one with the latest activity, which is also the copy the
  session detail view opens. The `machines` field, shown in the Machine
  column, lists every machine holding a copy.

A record found in several files is attributed to the file modified earliest,
the original copy that stopped being written once the session was copied.

Daily aggregates are rewritten for every day whose totals change, so files
that arrive late, or sessions that add records to earlier days, update the
history while claumon runs. A day whose recomputed message count is lower
than the stored one is left as stored: some of its transcripts were deleted
(Claude Code removes old ones), and the store keeps the history.

## History per machine

claumon also stores the daily totals of each machine (table
`daily_machine_aggregates`), kept like the overall history and for the same
`retention_days`.

In the dashboard, the Machine selector in the top bar narrows the whole page
to one machine: today's cards, the hourly heatmap, the daily charts, the
session list, the stored transcripts, and the running processes (shown only
for the machine claumon runs on). The usage gauges belong to the Claude
account, shared by all machines, and are not filtered.

The API offers `GET /api/machines` (machines with stored history) and a
`machine` parameter on `GET /api/today`, `GET /api/heatmap`,
`GET /api/history` and `GET /api/sessions`. `GET /api/info` reports the
local machine's name as `local_machine`.

A record present on several machines counts for the machine whose copy was
modified first, so the machines of a day add up to the day's total. History
is stored under the name shown at the time: renaming a machine with
`machine_aliases` starts a new history under the new name, and the old name
stays in the list until its days expire.

## Disk space

The session files collected under `machines_dir` are claumon's long-term
archive: claumon never deletes them. The dashboard's Machines panel shows,
for each machine, when its agent last sent new session data and last
contacted the server (agents ask about the usage gauges every 30 seconds
while they run, so this tells whether a machine's agent is up), and the disk
space used by its directory (every file in it, including those claumon does
not read). The same is available as `GET /api/storage`, and the total size
is logged at startup. Set `max_storage_mb` to get a warning
banner when the total goes above it:

```json
{
  "machines_dir": "~/claumon-machines",
  "max_storage_mb": 4096
}
```

The limit only warns; freeing space is up to you.

With agents (`ingest_tokens` set), the server can also compress the files of
inactive sessions: with `"compress_after_days": 1`, every hour it compresses
the session files not modified for a day into `<name>.jsonl.gz`, which
claumon reads like the plain files. On real transcripts, already slimmed by
the agents, this divided their size by about four. When a compressed session
resumes, the server restores the file before appending the new lines. Leave
the option off when the files are copied with `rsync`: no longer finding the
original files on the server, rsync would copy them again in full.

## Usage gauges

The usage gauges (5-hour session, weekly limits) come from Anthropic's usage
API, which needs the Claude credentials that Claude Code keeps fresh on the
machines where it runs. A server that does not run Claude Code has none, so
its agents fetch the gauges for it: every 30 seconds each agent asks the
server whether a reading is due, and the server says yes to one agent at a
time when a dashboard was opened since the last reading (unless that reading
is under 2 minutes old), when its last reading is older than
`usage_refresh_minutes` (default 60) while a dashboard is open, or older than
`usage_idle_refresh_hours` (default 6) otherwise. An agent whose machine has no valid credentials (Claude Code
not used there for a while) passes, and another one answers. Readings are
stored and forecast like the server's own, and the server reports the
subscription of the agent's credentials. `claumon agent --no-usage` opts a
machine out.

```json
{
  "usage_refresh_minutes": 60,
  "usage_idle_refresh_hours": 6
}
```

## Limitations

- Running and stuck states, and the actions that interrupt a session, only
  apply to processes on the machine claumon runs on.
- Memory files are only read from the local `~/.claude`; agents do not send
  them. A multi-machine server without memory files of its own hides the
  Memories and Graph tabs.
