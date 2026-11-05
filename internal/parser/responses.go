package parser

import "time"

// apiResponse is the token usage of one assistant API response.
type apiResponse struct {
	ID          string // message ID; empty for legacy lines without one
	Time        time.Time
	Model       string
	Input       int
	Output      int
	CacheRead   int
	CacheCreate int
}

// responseLog collects one entry per assistant API response in a session
// file. Claude Code writes a separate JSONL line for each content block of a
// response (text, thinking, each tool_use), and every one of those lines
// repeats the response's full usage, so summing lines overcounts. Lines are
// deduplicated by message ID; the last line wins. Lines without an ID are
// kept as-is.
type responseLog struct {
	entries []apiResponse
	index   map[string]int
}

func (l *responseLog) add(entry *jsonlLine) {
	if entry.Message == nil || entry.Message.Usage == nil {
		return
	}
	u := entry.Message.Usage
	r := apiResponse{
		ID:          entry.Message.ID,
		Time:        entry.Timestamp,
		Model:       entry.Message.Model,
		Input:       u.InputTokens,
		Output:      u.OutputTokens,
		CacheRead:   u.CacheReadInputTokens,
		CacheCreate: u.CacheCreationInputTokens,
	}
	id := entry.Message.ID
	if id == "" {
		l.entries = append(l.entries, r)
		return
	}
	if l.index == nil {
		l.index = map[string]int{}
	}
	if i, ok := l.index[id]; ok {
		l.entries[i] = r
		return
	}
	l.index[id] = len(l.entries)
	l.entries = append(l.entries, r)
}
