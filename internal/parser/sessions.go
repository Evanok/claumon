package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/memory"
	"github.com/fabioconcina/claumon/internal/pricing"
)

// warnedModels tracks models we've already warned about to avoid log spam.
var warnedModels sync.Map

// pricingTable is the shared pricing table, set via SetPricingTable.
// If nil, a zero-cost fallback is used (should not happen in practice).
var pricingTable *pricing.Table

type SessionSummary struct {
	ID                string    `json:"id"`
	Project           string    `json:"project"`
	Model             string    `json:"model"`
	Title             string    `json:"title"`
	StartTime         time.Time `json:"start_time"`
	LastActivity      time.Time `json:"last_activity"`
	InputTokens       int       `json:"input_tokens"`
	OutputTokens      int       `json:"output_tokens"`
	CacheReadTokens   int       `json:"cache_read_tokens"`
	CacheCreateTokens int       `json:"cache_create_tokens"`
	EstimatedCostUSD  float64   `json:"estimated_cost_usd"`
	MessageCount      int       `json:"message_count"`
	CWD               string    `json:"cwd"`
	// Machine is the machine the session file was collected from; empty for
	// the local machine, so single-machine API output is unchanged.
	Machine         string    `json:"machine,omitempty"`
	// Machines lists the named machines holding a copy of the session.
	Machines        []string  `json:"machines,omitempty"`
	HasFileEdits    bool      `json:"has_file_edits"`
	CacheEfficiency float64   `json:"cache_efficiency"`
	WasteFlags      []string  `json:"waste_flags"`
	ContextLength   int       `json:"context_length"`
	IsRunning       bool      `json:"is_running"`
	IsStuck         bool      `json:"is_stuck"`
	PID             int       `json:"pid,omitempty"`
	IdleMinutes     float64   `json:"idle_minutes,omitempty"`
}

// SessionMessage represents a single parsed message from a session for the detail view.
type SessionMessage struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Model     string    `json:"model,omitempty"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	TokensIn  int       `json:"tokens_in,omitempty"`
	TokensOut int       `json:"tokens_out,omitempty"`
	CacheRead   int       `json:"cache_read,omitempty"`
	CacheCreate int       `json:"cache_create,omitempty"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	Thinking    string    `json:"thinking,omitempty"`
}

// ToolCall represents a single tool_use invocation from an assistant message,
// optionally paired with its matching tool_result from the following user message.
type ToolCall struct {
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

type jsonlLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"sessionId"`
	UUID      string    `json:"uuid"`
	CWD       string    `json:"cwd"`
	Message   *struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Role    string `json:"role"`
		Content json.RawMessage `json:"content,omitempty"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Title string `json:"title,omitempty"`
}

// newSessionScanner creates a scanner for reading JSONL session files with a 2MB line limit.
func newSessionScanner(f *os.File) *bufio.Scanner {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 256*1024), 10*1024*1024) // 10MB: Claude sessions can have very large tool-result lines
	return s
}

func ParseSessionFile(path string) (*SessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	summary := &SessionSummary{
		ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}

	scanner := newSessionScanner(f)

	var firstUserMsg string
	var responses responseLog

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry jsonlLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		ts := entry.Timestamp
		if !ts.IsZero() {
			if summary.StartTime.IsZero() || ts.Before(summary.StartTime) {
				summary.StartTime = ts
			}
			if ts.After(summary.LastActivity) {
				summary.LastActivity = ts
			}
		}

		if entry.SessionID != "" && summary.ID == "" {
			summary.ID = entry.SessionID
		}

		if entry.CWD != "" && summary.CWD == "" {
			summary.CWD = entry.CWD
		}

		switch entry.Type {
		case "ai-title":
			if entry.Title != "" {
				summary.Title = entry.Title
			}
		case "assistant":
			if entry.Message != nil {
				summary.MessageCount++
				if entry.Message.Model != "" {
					summary.Model = entry.Message.Model
				}
				if u := entry.Message.Usage; u != nil {
					responses.add(&entry)
					summary.ContextLength = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
				}
				if !summary.HasFileEdits && hasFileEditTool(entry.Message.Content) {
					summary.HasFileEdits = true
				}
			}
		case "user":
			summary.MessageCount++
			if firstUserMsg == "" && entry.Message != nil {
				firstUserMsg = extractText(entry.Message.Content)
			}
		}
	}

	for _, r := range responses.entries {
		summary.InputTokens += r.Input
		summary.OutputTokens += r.Output
		summary.CacheReadTokens += r.CacheRead
		summary.CacheCreateTokens += r.CacheCreate
	}

	// Fallback title: first user message, stripped of XML tags, truncated
	if summary.Title == "" && firstUserMsg != "" {
		summary.Title = truncate(stripXMLTags(firstUserMsg), 80)
	}

	summary.EstimatedCostUSD = estimateCost(summary)
	summary.CacheEfficiency = cacheEfficiency(summary)
	summary.WasteFlags = detectWaste(summary)
	// Don't fail the whole session on scanner errors (e.g. lines exceeding buffer).
	// We still have valid data from everything parsed before the error.
	return summary, nil
}

// SetPricingTable sets the shared pricing table used for cost estimation.
func SetPricingTable(t *pricing.Table) {
	pricingTable = t
}

func estimateCost(s *SessionSummary) float64 {
	cost := costFor(s.Model, s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreateTokens)
	return math.Round(cost*10000) / 10000
}

// costFor returns the unrounded USD cost for the given token mix under the
// given model's pricing. Returns 0 if pricing is unavailable.
func costFor(model string, in, out, cacheRead, cacheCreate int) float64 {
	if pricingTable == nil {
		return 0
	}
	norm := normalizeModel(model)
	p, ok := pricingTable.Get(norm)
	if !ok {
		if _, warned := warnedModels.LoadOrStore(model, true); !warned {
			log.Printf("[pricing] Unknown model %q — using sonnet pricing. Update pricing.json?", model)
		}
		p, _ = pricingTable.Get("claude-sonnet-5")
	}
	return float64(in)/1e6*p.Input +
		float64(out)/1e6*p.Output +
		float64(cacheRead)/1e6*p.CacheRead +
		float64(cacheCreate)/1e6*p.CacheWrite5m
}

func normalizeModel(model string) string {
	if pricingTable == nil {
		return "claude-sonnet-5"
	}

	// Strip date suffixes like "claude-sonnet-4-6-20250514". Prefer the
	// longest matching key so "claude-fable-5-1" resolves to itself rather
	// than to the "claude-fable-5" prefix.
	best := ""
	for key := range pricingTable.Models() {
		if strings.HasPrefix(model, key) && len(key) > len(best) {
			best = key
		}
	}
	if best != "" {
		return best
	}
	// Try matching by family: pick the latest known model in each family
	switch {
	case strings.Contains(model, "fable"):
		return "claude-fable-5-1"
	case strings.Contains(model, "mythos"):
		return "claude-mythos-5-1"
	case strings.Contains(model, "opus"):
		return "claude-opus-5"
	case strings.Contains(model, "haiku"):
		return "claude-haiku-4-5"
	default:
		return "claude-sonnet-5"
	}
}

type sessionFile struct {
	path    string
	project string
	machine string // "" for the local machine
	modTime time.Time
	size    int64
}

// enumerateSessionFiles walks claudeDir/projects/*/ for *.jsonl and returns
// their paths, decoded project names, and mtimes. Stat failures are skipped silently.
//
// When a machines directory is set (SetMachinesDir), every
// <machinesDir>/<machine>/projects/*/ is walked too and its files are tagged
// with that machine. In that mode a missing local projects directory is not
// an error, since a server aggregating other machines may not run Claude Code
// itself; an error is returned only when no projects directory is readable.
func enumerateSessionFiles(claudeDir string) ([]sessionFile, error) {
	var files []sessionFile
	var firstErr error
	readable := 0
	for _, root := range projectsRoots(claudeDir) {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reading projects directory: %w", err)
			}
			continue
		}
		readable++
		files = appendProjectFiles(files, root, entries)
	}
	// Without a machines directory there is a single root, so this keeps the
	// historical "missing projects directory is an error" behavior.
	if readable == 0 {
		return nil, firstErr
	}

	// Enumeration is the one place every discovery path passes through, so
	// piggyback cache eviction of deleted files here.
	summaryCache.prune(files)
	ledgerCache.prune(files)

	return files, nil
}

// appendProjectFiles appends the *.jsonl files of each project directory in
// entries (the listing of root.dir) to files.
func appendProjectFiles(files []sessionFile, root projectsRoot, entries []os.DirEntry) []sessionFile {
	for _, projEntry := range entries {
		if !projEntry.IsDir() {
			continue
		}
		projPath := filepath.Join(root.dir, projEntry.Name())
		projName := memory.DecodePath(projEntry.Name())

		matches, err := filepath.Glob(filepath.Join(projPath, "*.jsonl"))
		if err != nil {
			continue
		}
		for _, f := range matches {
			info, err := os.Stat(f)
			if err != nil {
				continue
			}
			files = append(files, sessionFile{
				path:    f,
				project: projName,
				machine: root.machine,
				modTime: info.ModTime(),
				size:    info.Size(),
			})
		}
	}
	return files
}

// summaryCache memoizes ParseSessionFile results. Summaries are stored by
// value and copied out on every hit because callers mutate them (Project,
// process-status enrichment).
var summaryCache = newFileCache[SessionSummary]()

// parseSummaryByValue adapts ParseSessionFile to the value semantics the
// summary cache stores.
func parseSummaryByValue(path string) (SessionSummary, error) {
	s, err := ParseSessionFile(path)
	if err != nil {
		return SessionSummary{}, err
	}
	return *s, nil
}

// parseSessionFileCached is a cached ParseSessionFile for callers that have a
// bare path rather than an enumeration entry. The returned summary is a copy.
func parseSessionFileCached(path string) (SessionSummary, error) {
	info, err := os.Stat(path)
	if err != nil {
		return SessionSummary{}, err
	}
	return summaryCache.get(
		sessionFile{path: path, modTime: info.ModTime(), size: info.Size()},
		parseSummaryByValue)
}

// parseSessionFiles parses each file and returns one summary per session,
// with project and machine tagged. Files sharing a session ID are merged (see
// mergeSessionCopies); empty files are skipped. Sessions are returned in the
// order their first file appears in files.
func parseSessionFiles(files []sessionFile) []*SessionSummary {
	groups := map[string][]sessionCopy{}
	var order []string
	for _, f := range files {
		val, err := summaryCache.get(f, parseSummaryByValue)
		if err != nil || val.MessageCount == 0 {
			continue
		}
		s := val // copy out of the cache before mutating
		s.Project = f.project
		s.Machine = f.machine
		if _, ok := groups[s.ID]; !ok {
			order = append(order, s.ID)
		}
		groups[s.ID] = append(groups[s.ID], sessionCopy{file: f, summary: s})
	}

	sessions := make([]*SessionSummary, 0, len(order))
	for _, id := range order {
		s := mergeSessionCopies(groups[id])
		sessions = append(sessions, &s)
	}
	return sessions
}

func DiscoverSessions(claudeDir string) ([]*SessionSummary, error) {
	files, err := enumerateSessionFiles(claudeDir)
	if err != nil {
		return nil, err
	}
	return parseSessionFiles(files), nil
}

// DiscoverRecentSessions returns at most limit sessions, most recent activity
// first. This avoids parsing all JSONL files by only parsing the most recently
// modified ones, plus the other copies of the same sessions so that their
// merged totals are complete.
func DiscoverRecentSessions(claudeDir string, limit int) ([]*SessionSummary, error) {
	files, err := enumerateSessionFiles(claudeDir)
	if err != nil {
		return nil, err
	}
	// Sort by modification time descending (most recent first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	if len(files) > limit {
		recent := map[string]bool{}
		for _, f := range files[:limit] {
			recent[sessionIDFromPath(f.path)] = true
		}
		selected := files[:limit:limit]
		for _, f := range files[limit:] {
			if recent[sessionIDFromPath(f.path)] {
				selected = append(selected, f)
			}
		}
		files = selected
	}
	sessions := parseSessionFiles(files)
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

// DiscoverTodaySessions returns only sessions with activity today.
func DiscoverTodaySessions(claudeDir string) ([]*SessionSummary, error) {
	all, err := DiscoverSessions(claudeDir)
	if err != nil {
		return nil, err
	}

	today := time.Now().Truncate(24 * time.Hour)
	var result []*SessionSummary
	for _, s := range all {
		if s.LastActivity.After(today) || s.StartTime.After(today) {
			result = append(result, s)
		}
	}
	return result, nil
}

// ParseSessionDetail returns the full message timeline for a session.
func ParseSessionDetail(path string) ([]SessionMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	var messages []SessionMessage
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

		switch entry.Type {
		case "user":
			if entry.Message == nil {
				continue
			}
			// Attach tool_results to the matching tool_use on the most
			// recent preceding assistant message, and drop this user entry
			// if it only carried tool_results (no user-visible text).
			if results := extractToolResults(entry.Message.Content); len(results) > 0 {
				for i := len(messages) - 1; i >= 0; i-- {
					if messages[i].Role != "assistant" {
						continue
					}
					for j := range messages[i].ToolCalls {
						if r, ok := results[messages[i].ToolCalls[j].ID]; ok {
							messages[i].ToolCalls[j].Result = r.Text
							messages[i].ToolCalls[j].IsError = r.IsError
						}
					}
					break
				}
			}
			if hasOnlyToolResults(entry.Message.Content) {
				continue
			}
			msg := SessionMessage{
				Type:      "user",
				Timestamp: entry.Timestamp,
				Role:      "user",
				Text:      extractText(entry.Message.Content),
			}
			if msg.Text != "" {
				messages = append(messages, msg)
			}

		case "assistant":
			if entry.Message == nil {
				continue
			}
			msg := SessionMessage{
				Type:      "assistant",
				Timestamp: entry.Timestamp,
				Role:      "assistant",
				Model:     entry.Message.Model,
			}
			if entry.Message.Usage != nil {
				msg.TokensIn = entry.Message.Usage.InputTokens
				msg.TokensOut = entry.Message.Usage.OutputTokens
				msg.CacheRead = entry.Message.Usage.CacheReadInputTokens
				msg.CacheCreate = entry.Message.Usage.CacheCreationInputTokens
			}
			msg.Text = extractText(entry.Message.Content)
			msg.ToolCalls = extractToolCalls(entry.Message.Content)
			msg.Thinking = extractThinking(entry.Message.Content)
			// Skip empty assistant messages (e.g. thinking-only with no
			// visible content — Claude Code persists only the signature,
			// not the thinking text).
			if msg.Text == "" && len(msg.ToolCalls) == 0 && msg.Thinking == "" {
				continue
			}
			messages = append(messages, msg)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning session detail: %w", err)
	}
	return messages, nil
}

// FindSessionFile finds the JSONL file for a session ID, searching the local
// projects directory and each machine (see SetMachinesDir). When the session
// has several copies, the most complete one is returned (see primaryCopy).
func FindSessionFile(claudeDir, sessionID string) string {
	var copies []sessionCopy
	for _, root := range projectsRoots(claudeDir) {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			f, err := singleFile(filepath.Join(root.dir, e.Name(), sessionID+".jsonl"))
			if err != nil {
				continue
			}
			// An unparsable copy still competes, with a zero summary.
			s, _ := summaryCache.get(f, parseSummaryByValue)
			copies = append(copies, sessionCopy{file: f, summary: s})
		}
	}
	if len(copies) == 0 {
		return ""
	}
	return copies[primaryCopy(copies)].file.path
}

// ParseSessionFileDaily parses a session JSONL and buckets each assistant
// message's token usage by its own timestamp's local date. The returned map
// is keyed by "YYYY-MM-DD". Both user and assistant messages contribute to
// MessageCount on their own date. SessionCount is left at 0 (set by caller).
func ParseSessionFileDaily(path string) (map[string]*SessionAggregate, error) {
	f, err := singleFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := ledgerCache.get(f, parseSessionLedger); err != nil {
		return nil, err
	}
	buckets := map[string]*SessionAggregate{}
	for date, agg := range dailyAggregates([]sessionFile{f}) {
		agg.SessionCount = 0
		buckets[date] = &agg
	}
	return buckets, nil
}

// HourlyTokensToday returns a 24-element slice where index i holds the total
// tokens (input + output + cache read + cache create) from assistant messages
// whose timestamp fell in local hour i on the current local day. Messages from
// other days are skipped, even if they belong to a session that was also
// active today. Responses present in several files are counted once.
func HourlyTokensToday(claudeDir string) ([24]int, error) {
	files, err := enumerateSessionFiles(claudeDir)
	if err != nil {
		return [24]int{}, err
	}
	return hourlyTokens(files, localDate(time.Now())), nil
}

// DiscoverDailyAggregates walks all session files and returns aggregates
// bucketed by message timestamp (local date). Records present in several
// files (a session copied to another project directory or collected from
// several machines) are counted once. SessionCount on each entry is the
// number of distinct session IDs that contributed a counted record on that
// date.
func DiscoverDailyAggregates(claudeDir string) (map[string]SessionAggregate, error) {
	files, err := enumerateSessionFiles(claudeDir)
	if err != nil {
		return nil, err
	}
	return dailyAggregates(files), nil
}

// SessionAggregate holds aggregated token counts and cost across sessions.
type SessionAggregate struct {
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CacheReadTokens   int     `json:"cache_read_tokens"`
	CacheCreateTokens int     `json:"cache_create_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	SessionCount      int     `json:"session_count"`
	MessageCount      int     `json:"message_count"`
}

// contentBlock represents a single block in a Claude message content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// parseContentBlocks parses message content as either a string or array of blocks.
// Returns nil if content is empty or unparseable.
func parseContentBlocks(content json.RawMessage) []contentBlock {
	if len(content) == 0 {
		return nil
	}

	// Try as string first
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}

	// Try as array of content blocks
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err == nil {
		return blocks
	}
	return nil
}

func extractText(content json.RawMessage) string {
	blocks := parseContentBlocks(content)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractThinking(content json.RawMessage) string {
	blocks := parseContentBlocks(content)
	var parts []string
	for _, b := range blocks {
		if b.Type == "thinking" && b.Thinking != "" {
			parts = append(parts, b.Thinking)
		}
	}
	return strings.Join(parts, "\n")
}

func extractToolCalls(content json.RawMessage) []ToolCall {
	blocks := parseContentBlocks(content)
	var calls []ToolCall
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			calls = append(calls, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	return calls
}

// toolResult is an intermediate struct used when pairing tool_result blocks
// from user messages back to their originating tool_use calls.
type toolResult struct {
	Text    string
	IsError bool
}

// extractToolResults returns tool_result blocks keyed by their tool_use_id.
// Result content may be a plain string or an array of text blocks — both are
// normalized to a single string and truncated to avoid bloating the API payload.
func extractToolResults(content json.RawMessage) map[string]toolResult {
	blocks := parseContentBlocks(content)
	if len(blocks) == 0 {
		return nil
	}
	out := map[string]toolResult{}
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		text := normalizeResultContent(b.Content)
		if len(text) > 4096 {
			text = text[:4096] + "\n\n… (truncated)"
		}
		out[b.ToolUseID] = toolResult{Text: text, IsError: b.IsError}
	}
	return out
}

// normalizeResultContent handles the two shapes tool_result.content can take:
// a bare string, or an array of {type:"text", text:"..."} blocks.
func normalizeResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// hasOnlyToolResults reports whether a content payload contains tool_result
// blocks and nothing else worth displaying as a user message bubble.
func hasOnlyToolResults(content json.RawMessage) bool {
	blocks := parseContentBlocks(content)
	if len(blocks) == 0 {
		return false
	}
	sawToolResult := false
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			sawToolResult = true
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				return false
			}
		default:
			return false
		}
	}
	return sawToolResult
}

var (
	xmlTagRe     = regexp.MustCompile(`<[^>]+>`)
	fullTagLineRe = regexp.MustCompile(`(?m)^<\w[^>]*>.*?</\w+>\s*`)
)

// stripXMLTags removes XML/HTML tags and their content when the tag wraps the entire
// line (like <ide_opened_file>...</ide_opened_file>), then removes any remaining tags.
func stripXMLTags(s string) string {
	s = fullTagLineRe.ReplaceAllString(s, "")
	s = xmlTagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// fileEditTools are tool names that indicate the session produced file changes.
var fileEditTools = map[string]bool{
	"Write": true, "Edit": true, "NotebookEdit": true,
	"write": true, "edit": true,
}

// hasFileEditTool checks if a message's content blocks contain a file-editing tool_use.
func hasFileEditTool(content json.RawMessage) bool {
	for _, b := range parseContentBlocks(content) {
		if b.Type == "tool_use" && fileEditTools[b.Name] {
			return true
		}
	}
	return false
}

// cacheEfficiency returns the fraction of input context served from existing cache.
// Computed as cache_read / (input + cache_read + cache_create).
// Returns 0 if there is no input context.
func cacheEfficiency(s *SessionSummary) float64 {
	total := s.InputTokens + s.CacheReadTokens + s.CacheCreateTokens
	if total == 0 {
		return 0
	}
	ratio := float64(s.CacheReadTokens) / float64(total)
	return math.Round(ratio*1000) / 1000
}

const (
	wasteMinTokens      = 50_000
	wasteMinMessages    = 10
	wasteLowEfficiency  = 0.5
)

// detectWaste flags sessions with low cache efficiency or high token
// usage without any file edits.
func detectWaste(s *SessionSummary) []string {
	totalTokens := s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreateTokens
	var flags []string

	if s.CacheEfficiency <= wasteLowEfficiency && totalTokens >= wasteMinTokens {
		flags = append(flags, "low_cache_efficiency")
	}

	if !s.HasFileEdits && totalTokens >= wasteMinTokens && s.MessageCount >= wasteMinMessages {
		flags = append(flags, "no_file_edits")
	}

	if flags == nil {
		flags = []string{}
	}
	return flags
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

