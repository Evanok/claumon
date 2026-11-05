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

- The machine name is the subdirectory name. It is shown in the dashboard next
  to the project of each session.
- Only `<machine>/projects/<project>/*.jsonl` files are read. Other files and
  directories are ignored, and so are entries whose name starts with `.`
  (handy for temporary files written during a copy).
- The local `~/.claude/projects` is still read, and its sessions have no
  machine name. A server that does not run Claude Code itself can have no
  local `projects` directory at all.

claumon only reads this directory. Copying the files there is up to you, for
example with `rsync` from each machine:

```bash
rsync -a ~/.claude/projects/ server:claumon-machines/office-laptop/projects/
```

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
  session detail view opens. The `machines` field lists every named machine
  holding a copy.

A record found in several files is attributed to the file modified earliest,
the original copy that stopped being written once the session was copied.

Daily aggregates are rewritten for every day whose totals change, so files
that arrive late, or sessions that add records to earlier days, update the
history while claumon runs.

## Limitations

- Usage gauges (rate limits) come from the credentials of the machine claumon
  runs on, not from the other machines.
- Running and stuck states, and the actions that interrupt a session, only
  apply to processes on the machine claumon runs on.
- Memory files are only read from the local `~/.claude`.
