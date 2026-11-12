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
	UpsertMachineDailyAggregate(machine string, agg store.DailyAggregate) error
	MachineDailyMessageCounts() (map[store.MachineDay]int, error)
}

// dailyAggregateSync keeps the daily_aggregates table, and in multi-machine
// mode the daily_machine_aggregates table, in step with the session files.
// Each sync recomputes every day (cheap: unchanged files are served from the
// parser caches) and writes only the rows whose value changed since the last
// successful write. The first sync therefore writes every day (the startup
// backfill), and later ones write today plus any past day that changed, for
// example when a resumed or copied session adds records to an earlier date,
// or a machine's files arrive late.
//
// The store keeps history after Claude Code deletes old transcripts (by
// default those of sessions inactive for 30 days). Days that no longer
// appear in the files are left in the store, and so are days whose
// transcripts were only partly deleted: when a row's recomputed message
// count is lower than the stored one, records are missing from the files,
// so the stored totals are kept. A row whose message count is unchanged or
// higher is rewritten, so added records (late files, subagent transcripts)
// and price changes are applied.
//
// A machine's row can also lose records without any deletion: a record
// found on several machines counts for the copy modified first, so a copy
// arriving later with an older modification time moves records between
// machines. Machine rows therefore keep their stored totals only on days
// whose overall total lost messages too, and a stored machine row that no
// longer has any record is reset to zero when its day is otherwise intact.
type dailyAggregateSync struct {
	claudeDir string
	st        aggregateStore

	mu sync.Mutex // serializes syncs (watcher and startup can overlap)
	// days and machineDays are nil until their stored message counts are
	// loaded, on the first sync that needs them.
	days        *aggregateRows[string]
	machineDays *aggregateRows[store.MachineDay]
}

func newDailyAggregateSync(claudeDir string, st aggregateStore) *dailyAggregateSync {
	return &dailyAggregateSync{claudeDir: claudeDir, st: st}
}

// aggregateRows tracks the rows of one aggregates table, keyed by K.
type aggregateRows[K comparable] struct {
	// written holds the last computed value of each row that was written or
	// deliberately kept.
	written map[K]parser.SessionAggregate
	// stored is the message count stored per row.
	stored map[K]int
}

func newAggregateRows[K comparable](stored map[K]int) *aggregateRows[K] {
	return &aggregateRows[K]{written: map[K]parser.SessionAggregate{}, stored: stored}
}

type rowUpdate int

const (
	rowUnchanged rowUpdate = iota
	rowKept                // fewer messages than stored: transcripts deleted
	rowWritten
	rowFailed // write failed, retried on the next sync
)

// update applies the sync rules to one row, calling write when the row must
// be written. keepLower keeps the stored totals of a row whose message count
// went down.
func (r *aggregateRows[K]) update(key K, agg parser.SessionAggregate, keepLower bool, write func() error) rowUpdate {
	if prev, ok := r.written[key]; ok && prev == agg {
		return rowUnchanged
	}
	if stored, ok := r.stored[key]; ok && keepLower && agg.MessageCount < stored {
		r.written[key] = agg
		return rowKept
	}
	if err := write(); err != nil {
		// Not recorded as written, so the next sync retries it.
		log.Printf("[aggregate] %v", err)
		return rowFailed
	}
	r.written[key] = agg
	r.stored[key] = agg.MessageCount
	return rowWritten
}

// sync writes the changed days and returns how many days were written to
// daily_aggregates.
func (s *dailyAggregateSync) sync() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count, days := s.syncDays()
	if days != nil {
		// Without the day totals, deleted transcripts could lower machine
		// rows; they wait for the next sync.
		s.syncMachineDays(days)
	}
	return count
}

// dayStatus tells, per computed day, whether its transcripts were partly
// deleted (fewer messages than stored).
type dayStatus map[string]bool

// syncDays writes the changed days of daily_aggregates. It returns how many
// were written and the status of every computed day, nil when it failed.
func (s *dailyAggregateSync) syncDays() (int, dayStatus) {
	daily, err := parser.DiscoverDailyAggregates(s.claudeDir)
	if err != nil {
		log.Printf("[aggregate] Failed to compute daily aggregates: %v", err)
		return 0, nil
	}

	first := s.days == nil
	if first {
		stored, err := s.st.DailyMessageCounts()
		if err != nil {
			// Without the stored counts days could be lowered; retry on the
			// next sync.
			log.Printf("[aggregate] Failed to read stored aggregates: %v", err)
			return 0, nil
		}
		s.days = newAggregateRows(stored)
	}

	dates := make([]string, 0, len(daily))
	for date := range daily {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	today := time.Now().Format("2006-01-02")
	var pastDays, kept []string
	days := make(dayStatus, len(dates))
	count := 0
	for _, date := range dates {
		agg := daily[date]
		stored, ok := s.days.stored[date]
		days[date] = ok && agg.MessageCount < stored
		switch s.days.update(date, agg, true, func() error {
			return s.st.UpsertDailyAggregate(toStoreAggregate(date, agg))
		}) {
		case rowKept:
			kept = append(kept, date)
		case rowWritten:
			count++
			if date != today {
				pastDays = append(pastDays, date)
			}
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
	return count, days
}

// syncMachineDays writes the changed per-machine rows; a no-op in
// single-machine mode. Rows of partly deleted days keep their stored totals
// when they lost messages; stored rows left without records on an intact
// day are reset to zero.
func (s *dailyAggregateSync) syncMachineDays(days dayStatus) {
	perMachine, err := parser.DiscoverMachineDailyAggregates(s.claudeDir)
	if err != nil {
		log.Printf("[aggregate] Failed to compute machine aggregates: %v", err)
		return
	}
	if perMachine == nil {
		return
	}

	if s.machineDays == nil {
		stored, err := s.st.MachineDailyMessageCounts()
		if err != nil {
			log.Printf("[aggregate] Failed to read stored machine aggregates: %v", err)
			return
		}
		s.machineDays = newAggregateRows(stored)
	}

	keys := make([]store.MachineDay, 0, len(perMachine))
	for date, machines := range perMachine {
		for machine := range machines {
			keys = append(keys, store.MachineDay{Date: date, Machine: machine})
		}
	}
	for k := range s.machineDays.stored {
		partlyDeleted, present := days[k.Date]
		if _, computed := perMachine[k.Date][k.Machine]; present && !partlyDeleted && !computed {
			keys = append(keys, k) // its records moved to another machine
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Date != keys[j].Date {
			return keys[i].Date < keys[j].Date
		}
		return keys[i].Machine < keys[j].Machine
	})

	var kept []string
	for _, k := range keys {
		agg := perMachine[k.Date][k.Machine]
		if s.machineDays.update(k, agg, days[k.Date], func() error {
			return s.st.UpsertMachineDailyAggregate(k.Machine, toStoreAggregate(k.Date, agg))
		}) == rowKept {
			kept = append(kept, k.Date+"/"+k.Machine)
		}
	}
	if len(kept) > 0 {
		log.Printf("[aggregate] Kept stored totals of %d machine days whose transcripts were partly deleted: %s", len(kept), dateList(kept))
	}
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
