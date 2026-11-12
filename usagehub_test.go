package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/forecast"
	"github.com/fabioconcina/claumon/internal/ingest"
	"github.com/fabioconcina/claumon/internal/server"
	"github.com/fabioconcina/claumon/internal/store"
)

func newTestHub(t *testing.T) (*usageHub, *server.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	os.MkdirAll(filepath.Join(dir, "claude", "projects"), 0o755)
	srv := server.New(filepath.Join(dir, "claude"), st, os.DirFS(dir), []string{"example.com"})
	srv.Handlers.RemoteUsageTTL = 7 * time.Hour
	fc := forecast.NewService(&storeAdapter{st: st}, forecast.DefaultConfig())
	return newUsageHub(st, srv.Broker, srv.Handlers, fc, time.Hour, 6*time.Hour), srv, st
}

func TestUsageHubDue(t *testing.T) {
	hub, _, _ := newTestHub(t)
	viewers, connects := 0, 0
	hub.viewers = func() int { return viewers }
	hub.connects = func() int { return connects }
	t0 := time.Now()
	hub.last = t0

	steps := []struct {
		name     string
		at       time.Duration // after the last reading
		viewers  int
		connects int // dashboards opened since start
		want     bool
	}{
		{"fresh reading, dashboard open", 30 * time.Minute, 1, 0, false},
		{"an hour old, nobody watching", 61 * time.Minute, 0, 0, false},
		{"an hour old, dashboard open", 61 * time.Minute, 1, 0, true},
		{"another agent asks right after", 62 * time.Minute, 1, 0, false},
		{"the first agent never answered", 64 * time.Minute, 1, 0, true},
		{"six hours old, nobody watching", 6*time.Hour + time.Minute, 0, 0, true},
	}
	for _, st := range steps {
		viewers, connects = st.viewers, st.connects
		if got := hub.UsageDue(t0.Add(st.at)); got != st.want {
			t.Errorf("%s: due = %v, want %v", st.name, got, st.want)
		}
	}
}

func TestUsageHubDueOnOpen(t *testing.T) {
	hub, _, _ := newTestHub(t)
	connects := 3
	hub.viewers = func() int { return 1 }
	hub.connects = func() int { return connects }
	hub.seenConnects = 3
	t0 := time.Now()
	hub.last = t0

	// Nobody opened the dashboard since the last reading.
	if hub.UsageDue(t0.Add(10 * time.Minute)) {
		t.Error("due without a new dashboard")
	}
	// A dashboard opens one minute after a reading: too fresh to refetch.
	connects = 4
	if hub.UsageDue(t0.Add(time.Minute)) {
		t.Error("due right after a reading")
	}
	// Ten minutes after the reading, the opening asks for a new one.
	if !hub.UsageDue(t0.Add(10 * time.Minute)) {
		t.Error("not due after a dashboard opened")
	}
	// The reading arrives: the opening is served.
	body := []byte(`{"five_hour": {"utilization": 1}, "seven_day": {"utilization": 1}}`)
	if ok, err := hub.AcceptUsage(ingest.PushedUsage{Machine: "m", Body: body, FetchedAt: t0.Add(11 * time.Minute)}); !ok || err != nil {
		t.Fatalf("accept: %v, %v", ok, err)
	}
	if hub.UsageDue(t0.Add(20 * time.Minute)) {
		t.Error("due again without a new dashboard")
	}
}

func TestUsageHubAccept(t *testing.T) {
	hub, srv, st := newTestHub(t)
	body := []byte(`{"five_hour": {"utilization": 42.0, "resets_at": "2099-01-01T10:00:00+00:00"}, "seven_day": {"utilization": 17.0, "resets_at": "2099-01-05T10:00:00+00:00"}}`)
	now := time.Now()

	info := func() map[string]interface{} {
		w := httptest.NewRecorder()
		srv.Mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
		var m map[string]interface{}
		json.NewDecoder(w.Body).Decode(&m)
		return m
	}
	if got := info()["subscription_type"]; got != "" {
		t.Fatalf("subscription before any reading = %v", got)
	}

	ok, err := hub.AcceptUsage(ingest.PushedUsage{Machine: "laptop", Body: body, FetchedAt: now, SubscriptionType: "team", RateLimitTier: "max_5x"})
	if err != nil || !ok {
		t.Fatalf("accept: %v, %v", ok, err)
	}
	if _, has, _ := st.LatestSnapshotTime(); !has {
		t.Error("no snapshot saved")
	}
	w := httptest.NewRecorder()
	srv.Mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage", nil))
	var usage map[string]interface{}
	json.NewDecoder(w.Body).Decode(&usage)
	if usage["session_pct"] != 42.0 || usage["weekly_pct"] != 17.0 {
		t.Errorf("/api/usage = %v", usage)
	}
	m := info()
	if m["subscription_type"] != "team" || m["auth_status"] != "ok" || m["is_api_billing"] != false {
		t.Errorf("/api/info after a reading = %v", m)
	}

	// An older reading (an agent that was slow to answer) is ignored.
	if ok, err := hub.AcceptUsage(ingest.PushedUsage{Machine: "home", Body: body, FetchedAt: now.Add(-time.Minute)}); err != nil || ok {
		t.Errorf("older reading accepted: %v, %v", ok, err)
	}
	if _, err := hub.AcceptUsage(ingest.PushedUsage{Machine: "home", Body: []byte("nope"), FetchedAt: now.Add(time.Minute)}); err == nil {
		t.Error("invalid reading accepted")
	}
	// After a reading, nothing is due for an hour.
	hub.viewers = func() int { return 1 }
	if hub.UsageDue(time.Now()) {
		t.Error("due right after a reading")
	}
}

func TestUsageHubRestoresAfterRestart(t *testing.T) {
	hub, _, st := newTestHub(t)
	body := []byte(`{"five_hour": {"utilization": 42.0, "resets_at": "2099-01-01T10:00:00+00:00"}, "seven_day": {"utilization": 17.0, "resets_at": "2099-01-05T10:00:00+00:00"}}`)
	if ok, err := hub.AcceptUsage(ingest.PushedUsage{Machine: "laptop", Body: body, FetchedAt: time.Now(), SubscriptionType: "team", RateLimitTier: "max_5x"}); !ok || err != nil {
		t.Fatalf("accept: %v, %v", ok, err)
	}

	// A new server on the same store, as after a restart.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "claude", "projects"), 0o755)
	srv := server.New(filepath.Join(dir, "claude"), st, os.DirFS(dir), []string{"example.com"})
	srv.Handlers.RemoteUsageTTL = 7 * time.Hour
	newUsageHub(st, srv.Broker, srv.Handlers, forecast.NewService(&storeAdapter{st: st}, forecast.DefaultConfig()), time.Hour, 6*time.Hour)

	get := func(url string) map[string]interface{} {
		w := httptest.NewRecorder()
		srv.Mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		var m map[string]interface{}
		json.NewDecoder(w.Body).Decode(&m)
		return m
	}
	if u := get("/api/usage"); u["session_pct"] != 42.0 || u["weekly_pct"] != 17.0 {
		t.Errorf("/api/usage after restart = %v", u)
	}
	if i := get("/api/info"); i["auth_status"] != "ok" || i["subscription_type"] != "team" {
		t.Errorf("/api/info after restart = %v", i)
	}
}
