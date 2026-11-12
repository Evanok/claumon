package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUsageSource(t *testing.T) {
	st := openTestStore(t)
	if _, ok, err := st.LastUsageSource(); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	st.SaveUsageSource(UsageSource{FetchedAt: t0, Machine: "office", Subscription: "team", Tier: "max_5x"})
	st.SaveUsageSource(UsageSource{FetchedAt: t0.Add(time.Hour), Machine: "home", Subscription: "team", Tier: "max_5x"})
	u, ok, err := st.LastUsageSource()
	if err != nil || !ok || u.Machine != "home" || !u.FetchedAt.Equal(t0.Add(time.Hour)) || u.Subscription != "team" {
		t.Errorf("source = %+v, %v, %v", u, ok, err)
	}
}

func TestLatestUsageSnapshot(t *testing.T) {
	st := openTestStore(t)
	if _, _, ok, err := st.LatestUsageSnapshot(); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	st.SaveUsageSnapshot(10, 20, "", "", json.RawMessage(`{"n":1}`))
	time.Sleep(1100 * time.Millisecond) // snapshot timestamps have one-second resolution
	st.SaveUsageSnapshot(11, 21, "", "", json.RawMessage(`{"n":2}`))
	raw, at, ok, err := st.LatestUsageSnapshot()
	if err != nil || !ok || string(raw) != `{"n":2}` || time.Since(at) > time.Minute {
		t.Errorf("latest = %s at %s, %v, %v", raw, at, ok, err)
	}
}
