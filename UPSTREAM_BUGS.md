# Upstream bugs found in this fork

Bugs present in upstream `fabioconcina/claumon` `main` (single-machine mode),
found while building multi-machine support on the `multi_machine_support`
branch. Each entry gives the symptom, how it was verified, and the fixing
commit when there is one, so it can become a separate pull request or an
issue for the maintainer.

This file is fork-only: leave it out of upstream pull requests.

## Fixed in the fork

### 1. Sessions copied into several files are counted twice

- **Symptom:** when a session moves to another project directory, Claude Code
  copies its history into a new file. Tokens, cost and messages of the copied
  part are counted twice, and the session shows as two rows in the session
  list.
- **Evidence:** real data, session `55150075` present in two project
  directories: 17 responses and 51 messages double counted.
- **Fix:** `e655b36` (count API responses once by message ID, message lines
  once by line UUID), `032ca89` (one row per session, built from its copies).

### 2. Only today's daily aggregate is updated after startup

- **Symptom:** after the startup backfill, a file change only rewrites
  today's row in `daily_aggregates`. Records added later to earlier days
  (a resumed session, a late file) never reach the history.
- **Evidence:** upstream `updateDailyAggregate` in `main.go` upserts
  `daily[today]` only.
- **Fix:** `9859e19` (rewrite every day whose totals changed).

### 3. Subagent transcripts are never read

- **Symptom:** the exchanges of the subagents a session runs are written to
  `<project>/<session>/subagents/*.jsonl`. claumon never reads them, so their
  tokens and cost are missing from every total.
- **Evidence:** real data, 30 days: about 2.92 USD and 57 API responses
  missing; none of these responses appear in the main transcripts.
- **Fix:** `0278061` (subagent responses add tokens and cost to the parent
  session, priced at the subagent's model; not counted as messages).

### 4. History capped at 90 days whatever `retention_days` keeps

- **Symptom:** `/api/history` refuses windows over 90 days and silently falls
  back to the default 14 days, although `retention_days` can keep more.
- **Evidence:** `HandleHistory` accepts `days` only when `n <= 90`.
- **Fix:** `61231ce` (serve up to the retention, shorten longer requests,
  180 / 365 / All in the UI with weekly or monthly bars).

### 5. Days whose transcripts were deleted are lowered at startup

- **Symptom:** the startup backfill upserts every day computed from the files
  still on disk. When Claude Code has deleted some of a day's transcripts
  (`cleanupPeriodDays`, 30 days by default), that day's stored totals drop.
- **Evidence:** upstream `backfillHistory` in `main.go` upserts every computed
  day unconditionally.
- **Fix:** `3f877c4` (keep the stored day when its recomputed message count
  is lower than the stored one).

### 6. The Today session list starts at midnight UTC

- **Symptom:** the Today session list keeps sessions active after
  `time.Now().Truncate(24 * time.Hour)`, which is midnight UTC, while the
  summary cards and daily aggregates use local midnight. West of UTC the list
  shows the previous evening's sessions; east of UTC it misses the first
  hours of the day.
- **Evidence:** `DiscoverTodaySessions` in `internal/parser/sessions.go`.
- **Fix:** `8208762` (local midnight).

### 7. The Total Tokens card does not say it is today's total

- **Symptom:** the other summary cards say "today"; the tokens card uses that
  line for the token breakdown, so nothing says it is a daily total.
- **Fix:** `fc6ee50` (label "Total Tokens Today").

### 12. Chart labels rounded to whole numbers on small scales

- **Symptom:** the y-axis ticks of the daily charts are rounded to whole
  numbers, and the cost labels above the bars to whole dollars. With small
  values (a light day, a single machine) the cost axis reads "0, 0, 1, 1, 1"
  and a $0.60 bar is labelled "$1".
- **Evidence:** `renderBarChart` and `renderBillableChart` in
  `web/index.html` use `fmtAxis(Math.round(...))` and `'$' + Math.round(val)`;
  seen on a screenshot of a $0.60 day.
- **Fix:** `a9fddd2`.

## Found, not fixed

### 8. The Recent list shows fewer sessions than its limit

- **Symptom:** the Recent list takes the 50 most recently modified files,
  then drops the empty ones, so it shows fewer than 50 sessions (46 on real
  data). Upstream, copies of a session also take one place each (see 1).
- **Status:** the fork's `0069e51` selects by session ID, which fixes the
  copies; empty files still take places.

### 9. Project names shown with a double leading slash

- **Symptom:** `memory.DecodePath("-skr-data2-arezzo")` returns
  `//skr/data2/arezzo`, shown as is in the project column.

### 10. Flaky store test

- **Symptom:** `TestConcurrentWritesWaitForLock` in `internal/store` failed
  once under `-race` with `database is locked (SQLITE_BUSY)`, 1 of 200
  writes; it passed on every rerun.

### 11. Go files not gofmt-formatted

- **Symptom:** several upstream files are not gofmt-clean, for example
  `internal/parser/sessions.go` and `internal/parser/sessions_test.go`.
