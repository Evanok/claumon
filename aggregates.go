package main

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
	"github.com/fabioconcina/claumon/internal/store"
)

// aggregateWriter is the store method dailyAggregateSync writes through; an
// interface so tests can observe the writes.
type aggregateWriter interface {
	UpsertDailyAggregate(agg store.DailyAggregate) error
}

// dailyAggregateSync keeps the daily_aggregates table in step with the
// session files. Each sync recomputes every day (cheap: unchanged files are
// served from the parser caches) and writes only the days whose value changed
// since the last successful write. The first sync therefore writes every day
// (the startup backfill), and later ones write today plus any past day that
// changed, for example when a resumed or copied session adds records to an
// earlier date, or a machine's files arrive late.
//
// Days that no longer appear in the files are left in the store: the store
// keeps history after Claude Code deletes old transcripts.
type dailyAggregateSync struct {
	claudeDir string
	st        aggregateWriter

	mu      sync.Mutex // serializes syncs (watcher and startup can overlap)
	written map[string]parser.SessionAggregate
}

func newDailyAggregateSync(claudeDir string, st aggregateWriter) *dailyAggregateSync {
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
		s.written = make(map[string]parser.SessionAggregate, len(daily))
	}

	dates := make([]string, 0, len(daily))
	for date := range daily {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	today := time.Now().Format("2006-01-02")
	var pastDays []string
	count := 0
	for _, date := range dates {
		agg := daily[date]
		if prev, ok := s.written[date]; ok && prev == agg {
			continue
		}
		if err := s.st.UpsertDailyAggregate(toStoreAggregate(date, agg)); err != nil {
			// Not recorded as written, so the next sync retries it.
			log.Printf("[aggregate] Failed to upsert aggregate for %s: %v", date, err)
			continue
		}
		s.written[date] = agg
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
	return count
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
