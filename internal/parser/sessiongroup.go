package parser

import (
	"path/filepath"
	"sort"
	"strings"
)

// The same session (same ID, hence same file name) can have several files:
// a copy in another project directory when the session moved, or a copy per
// machine in multi-machine mode. The session list shows one row per session,
// built by merging its copies.

// sessionCopy is one file of a session with its parsed summary.
type sessionCopy struct {
	file    sessionFile
	summary SessionSummary
}

// sessionIDFromPath returns the session ID encoded in a session file name.
func sessionIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// primaryCopy returns the index of the most complete copy: latest activity
// first, then largest file, then smallest path for determinism. A copy keeps
// receiving records after the others stop, so it is the most recent one.
func primaryCopy(copies []sessionCopy) int {
	best := 0
	for i := 1; i < len(copies); i++ {
		a, b := copies[i], copies[best]
		switch {
		case a.summary.LastActivity.After(b.summary.LastActivity):
			best = i
		case a.summary.LastActivity.Before(b.summary.LastActivity):
		case a.file.size > b.file.size:
			best = i
		case a.file.size < b.file.size:
		case a.file.path < b.file.path:
			best = i
		}
	}
	return best
}

// mergeSessionCopies builds the summary of a session from its copies. A
// single copy is returned as is. With several copies, descriptive fields
// (title, model, project, cwd, context length, machine) come from the primary
// copy, the time range spans all copies, and tokens and message counts are
// deduplicated across copies like the daily aggregates. Message counts then
// only include lines carrying a timestamp, which can differ by a few lines
// from the single-file count.
func mergeSessionCopies(copies []sessionCopy) SessionSummary {
	merged := copies[primaryCopy(copies)].summary
	merged.Machines = sessionMachines(copies)
	if len(copies) == 1 {
		return merged
	}

	files := make([]sessionFile, 0, len(copies))
	for _, c := range copies {
		files = append(files, c.file)
		s := c.summary
		if !s.StartTime.IsZero() && (merged.StartTime.IsZero() || s.StartTime.Before(merged.StartTime)) {
			merged.StartTime = s.StartTime
		}
		if s.LastActivity.After(merged.LastActivity) {
			merged.LastActivity = s.LastActivity
		}
		merged.HasFileEdits = merged.HasFileEdits || s.HasFileEdits
	}

	merged.InputTokens, merged.OutputTokens = 0, 0
	merged.CacheReadTokens, merged.CacheCreateTokens = 0, 0
	merged.MessageCount = 0
	visitUniqueRecords(files, ledgerVisitor{
		response: func(_ sessionFile, r apiResponse) {
			merged.InputTokens += r.Input
			merged.OutputTokens += r.Output
			merged.CacheReadTokens += r.CacheRead
			merged.CacheCreateTokens += r.CacheCreate
		},
		message: func(sessionFile, ledgerMessage) {
			merged.MessageCount++
		},
	})

	merged.EstimatedCostUSD = estimateCost(&merged)
	merged.CacheEfficiency = cacheEfficiency(&merged)
	merged.WasteFlags = detectWaste(&merged)
	return merged
}

// sessionMachines returns the sorted distinct named machines holding a copy,
// or nil when every copy is local.
func sessionMachines(copies []sessionCopy) []string {
	seen := map[string]bool{}
	var machines []string
	for _, c := range copies {
		m := c.file.machine
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		machines = append(machines, m)
	}
	sort.Strings(machines)
	return machines
}
