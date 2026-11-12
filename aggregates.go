package main

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
	"github.com/fabioconcina/claumon/internal/store"
)

// aggregateStore is the part of the store dailyAggregateSync uses; an
// interface so tests can observe the writes.
type aggregateStore interface {
	UpsertDailyAggregate(agg store.DailyAggregate) error
	DailyMessageCounts() (map[string]int, error)
}

// dailyAggregateSync keeps the daily_aggregates table in step with the
// session files. Each sync recomputes every day (cheap: unchanged files are
// served from the parser caches) and writes only the days whose value changed
// since the last successful write. The first sync therefore writes every day
// (the startup backfill), and later ones write today plus any past day that
// changed, for example when a resumed or copied session adds records to an
// earlier date, or a machine's files arrive late.
//
// The store keeps history after Claude Code deletes old transcripts (by
// default those of sessions inactive for 30 days). Days that no longer
// appear in the files are left in the store, and so are days whose
// transcripts were only partly deleted: when a day's recomputed message
// count is lower than the stored one, records are missing from the files,
// so the stored totals are kept. A day whose message count is unchanged or
// higher is rewritten, so added records (late files, subagent transcripts)
// and price changes are applied.
type dailyAggregateSync struct {
	claudeDir string
	st        aggregateStore

	mu sync.Mutex // serializes syncs (watcher and startup can overlap)
	// written holds, per day, the last computed value that was written or
	// deliberately kept; nil until the first successful sync.
	written map[string]parser.SessionAggregate
	// storedMessages is the message count stored per day.
	storedMessages map[string]int
}

func newDailyAggregateSync(claudeDir string, st aggregateStore) *dailyAggregateSync {
	return &dailyAggregateSync{claudeDir: claudeDir, st: st}
}

// sync writes the changed days and returns how many were written.
func (s *dailyAggregateSync) sync() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	daily, err := parser.DiscoverDailyAggregates(s.claudeDir)
	if err != nil {
		log.Printf("[aggregate] Failed to compute daily aggregates: %v", err)
		return 0
	}

	first := s.written == nil
	if first {
		stored, err := s.st.DailyMessageCounts()
		if err != nil {
			// Without the stored counts days could be lowered; retry on the
			// next sync.
			log.Printf("[aggregate] Failed to read stored aggregates: %v", err)
			return 0
		}
		s.storedMessages = stored
		s.written = make(map[string]parser.SessionAggregate, len(daily))
	}

	dates := make([]string, 0, len(daily))
	for date := range daily {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	today := time.Now().Format("2006-01-02")
	var pastDays, kept []string
	count := 0
	for _, date := range dates {
		agg := daily[date]
		if prev, ok := s.written[date]; ok && prev == agg {
			continue
		}
		if stored, ok := s.storedMessages[date]; ok && agg.MessageCount < stored {
			// Transcripts of that day were deleted: keep the stored totals.
			s.written[date] = agg
			kept = append(kept, date)
			continue
		}
		if err := s.st.UpsertDailyAggregate(toStoreAggregate(date, agg)); err != nil {
			// Not recorded as written, so the next sync retries it.
			log.Printf("[aggregate] Failed to upsert aggregate for %s: %v", date, err)
			continue
		}
		s.written[date] = agg
		s.storedMessages[date] = agg.MessageCount
		count++
		if date != today {
			pastDays = append(pastDays, date)
		}
	}

	switch {
	case first:
		log.Printf("[backfill] Done: %d days", count)
	case len(pastDays) > 0:
		log.Printf("[aggregate] Updated past days: %v", pastDays)
	}
	if len(kept) > 0 {
		log.Printf("[aggregate] Kept stored totals of %d days whose transcripts were partly deleted: %s", len(kept), dateList(kept))
	}
	return count
}

// dateList formats sorted dates for a log line, as a range when long.
func dateList(dates []string) string {
	if len(dates) <= 5 {
		return strings.Join(dates, ", ")
	}
	return dates[0] + " .. " + dates[len(dates)-1]
}

func toStoreAggregate(date string, a parser.SessionAggregate) store.DailyAggregate {
	return store.DailyAggregate{
		Date:              date,
		InputTokens:       a.InputTokens,
		OutputTokens:      a.OutputTokens,
		CacheReadTokens:   a.CacheReadTokens,
		CacheCreateTokens: a.CacheCreateTokens,
		CostUSD:           a.CostUSD,
		SessionCount:      a.SessionCount,
		MessageCount:      a.MessageCount,
	}
}
