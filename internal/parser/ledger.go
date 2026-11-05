package parser

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// A session's records can appear in more than one file: Claude Code copies
// the history into a new file when a session moves to another project
// directory, and in multi-machine mode the same session can be collected from
// several machines. Per-file totals therefore cannot simply be summed. Instead
// each file is reduced to a ledger of its records, and the ledgers of all
// files are merged with global deduplication: API responses by message ID
// (tokens and cost), message lines by line UUID (message counts).

// ledgerMessage is one user or assistant line, used for message counts.
type ledgerMessage struct {
	UUID string // empty for lines without one; such lines are never deduplicated
	Time time.Time
}

// fileLedger holds the timestamped records of one session file. Responses
// are already deduplicated within the file (see responseLog).
type fileLedger struct {
	responses []apiResponse
	messages  []ledgerMessage
}

// ledgerCache memoizes parseSessionLedger results. Cached ledgers are shared;
// callers must treat them as read-only.
var ledgerCache = newFileCache[fileLedger]()

// parseSessionLedger reads the records of a session file that carry a
// timestamp. Lines without one are skipped, as they cannot be bucketed.
func parseSessionLedger(path string) (fileLedger, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileLedger{}, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	var l fileLedger
	var responses responseLog
	scanner := newSessionScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry jsonlLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Timestamp.IsZero() || entry.Message == nil {
			continue
		}
		switch entry.Type {
		case "assistant":
			l.messages = append(l.messages, ledgerMessage{UUID: entry.UUID, Time: entry.Timestamp})
			responses.add(&entry)
		case "user":
			l.messages = append(l.messages, ledgerMessage{UUID: entry.UUID, Time: entry.Timestamp})
		}
	}
	l.responses = responses.entries
	return l, nil
}

// ledgerVisitor receives the records that survive global deduplication,
// together with the file they are attributed to.
type ledgerVisitor struct {
	response func(f sessionFile, r apiResponse)
	message  func(f sessionFile, m ledgerMessage)
}

// visitUniqueRecords merges the ledgers of files and calls v once per
// distinct response and message. When a record appears in several files it
// is attributed to the file modified earliest: the original copy stops being
// written once a session is copied elsewhere, so it is the oldest one. Ties
// are broken by path so the result is deterministic. Unreadable files are
// skipped.
func visitUniqueRecords(files []sessionFile, v ledgerVisitor) {
	ordered := append([]sessionFile(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].modTime.Equal(ordered[j].modTime) {
			return ordered[i].modTime.Before(ordered[j].modTime)
		}
		return ordered[i].path < ordered[j].path
	})

	seenResponses := map[string]bool{}
	seenMessages := map[string]bool{}
	for _, f := range ordered {
		l, err := ledgerCache.get(f, parseSessionLedger)
		if err != nil {
			continue
		}
		if v.response != nil {
			for _, r := range l.responses {
				if r.ID != "" {
					if seenResponses[r.ID] {
						continue
					}
					seenResponses[r.ID] = true
				}
				v.response(f, r)
			}
		}
		if v.message != nil {
			for _, m := range l.messages {
				if m.UUID != "" {
					if seenMessages[m.UUID] {
						continue
					}
					seenMessages[m.UUID] = true
				}
				v.message(f, m)
			}
		}
	}
}

// localDate formats t as a local "YYYY-MM-DD" bucket key.
func localDate(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// dailyAggregates buckets the deduplicated records of files by local date.
// SessionCount is the number of distinct session IDs that contributed at
// least one counted record on that date.
func dailyAggregates(files []sessionFile) map[string]SessionAggregate {
	type acc struct {
		agg      SessionAggregate
		sessions map[string]bool
	}
	perDay := map[string]*acc{}
	bucket := func(f sessionFile, t time.Time) *SessionAggregate {
		date := localDate(t)
		a := perDay[date]
		if a == nil {
			a = &acc{sessions: map[string]bool{}}
			perDay[date] = a
		}
		a.sessions[sessionIDFromPath(f.path)] = true
		return &a.agg
	}

	visitUniqueRecords(files, ledgerVisitor{
		response: func(f sessionFile, r apiResponse) {
			b := bucket(f, r.Time)
			b.InputTokens += r.Input
			b.OutputTokens += r.Output
			b.CacheReadTokens += r.CacheRead
			b.CacheCreateTokens += r.CacheCreate
			b.CostUSD += costFor(r.Model, r.Input, r.Output, r.CacheRead, r.CacheCreate)
		},
		message: func(f sessionFile, m ledgerMessage) {
			bucket(f, m.Time).MessageCount++
		},
	})

	out := make(map[string]SessionAggregate, len(perDay))
	for date, a := range perDay {
		a.agg.SessionCount = len(a.sessions)
		a.agg.CostUSD = math.Round(a.agg.CostUSD*10000) / 10000
		out[date] = a.agg
	}
	return out
}

// hourlyTokens returns the deduplicated token totals (input + output + cache
// read + cache create) of files for the local date, indexed by local hour.
func hourlyTokens(files []sessionFile, date string) [24]int {
	var hours [24]int
	visitUniqueRecords(files, ledgerVisitor{
		response: func(_ sessionFile, r apiResponse) {
			ts := r.Time.Local()
			if ts.Format("2006-01-02") != date {
				return
			}
			hours[ts.Hour()] += r.Input + r.Output + r.CacheRead + r.CacheCreate
		},
	})
	return hours
}

// singleFile builds the enumeration entry for a bare path.
func singleFile(path string) (sessionFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sessionFile{}, fmt.Errorf("opening session file: %w", err)
	}
	return sessionFile{path: path, modTime: info.ModTime(), size: info.Size()}, nil
}
