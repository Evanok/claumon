package main

import (
	"log"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/api"
	"github.com/fabioconcina/claumon/internal/auth"
	"github.com/fabioconcina/claumon/internal/forecast"
	"github.com/fabioconcina/claumon/internal/ingest"
	"github.com/fabioconcina/claumon/internal/server"
	"github.com/fabioconcina/claumon/internal/store"
)

// usageHub receives the usage readings agents fetch for the server
// (multi-machine mode, see ingest/usage.go) and handles them like readings
// of the server's own poller. It asks for a reading when a dashboard is
// opened, every openEvery while one stays open, and every idleEvery
// otherwise, from one agent at a time.
type usageHub struct {
	st       *store.Store
	broker   *server.SSEBroker
	handlers *server.Handlers
	forecast *forecast.Service

	openEvery, idleEvery time.Duration
	viewers              func() int // open dashboards
	connects             func() int // dashboards opened since start

	mu           sync.Mutex
	last         time.Time // when the newest reading was fetched
	claimedUntil time.Time // an agent was told to fetch; others wait
	seenConnects int       // connects() at the last reading
}

// usageClaim is how long the agent told to fetch has before another one is.
const usageClaim = 2 * time.Minute

// usageOnOpenMinAge is the age below which opening a dashboard does not ask
// for a new reading, so reloading the page does not hit the usage API.
const usageOnOpenMinAge = 2 * time.Minute

func newUsageHub(st *store.Store, broker *server.SSEBroker, handlers *server.Handlers, fc *forecast.Service, openEvery, idleEvery time.Duration) *usageHub {
	h := &usageHub{st: st, broker: broker, handlers: handlers, forecast: fc, openEvery: openEvery, idleEvery: idleEvery,
		viewers: broker.Clients, connects: broker.Connects}
	if last, ok, err := st.LatestSnapshotTime(); err == nil && ok {
		h.last = last
	}
	return h
}

// UsageDue reports whether an agent asking now should fetch a reading.
func (h *usageHub) UsageDue(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	age := now.Sub(h.last)
	opened := h.connects() > h.seenConnects // a dashboard was opened since the last reading
	wanted := age >= h.idleEvery ||
		(age >= h.openEvery && h.viewers() > 0) ||
		(age >= usageOnOpenMinAge && opened)
	if !wanted || now.Before(h.claimedUntil) {
		return false
	}
	h.claimedUntil = now.Add(usageClaim)
	return true
}

// AcceptUsage records a reading newer than the last one.
func (h *usageHub) AcceptUsage(u ingest.PushedUsage) (bool, error) {
	usage, err := api.ParseUsage(u.Body)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	if !u.FetchedAt.After(h.last) {
		h.mu.Unlock()
		return false, nil
	}
	h.last = u.FetchedAt
	h.claimedUntil = time.Time{}
	h.seenConnects = h.connects()
	h.mu.Unlock()

	if err := h.st.SaveUsageSnapshot(usage.SessionPercent, usage.WeeklyPercent, usage.SessionResetAt, usage.WeeklyResetAt, usage.Raw); err != nil {
		log.Printf("[usage] Failed to save usage snapshot: %v", err)
	}
	evt := buildUsageEvent(usage)
	attachForecasts(evt, usage, h.forecast)
	h.handlers.SetLatestUsage(evt)
	h.handlers.SetRemoteUsage(time.Now(), u.SubscriptionType, u.RateLimitTier)
	h.broker.SendJSON("usage", evt)
	h.broker.SendJSON("auth_status", map[string]string{"status": auth.AuthOK, "message": ""})
	log.Printf("[usage] Reading from %s: session=%.1f%% weekly=%.1f%%", u.Machine, usage.SessionPercent, usage.WeeklyPercent)
	return true, nil
}
