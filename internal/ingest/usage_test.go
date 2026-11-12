package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSink is a UsageSink recording what it receives.
type fakeSink struct {
	mu       sync.Mutex
	due      bool
	received []PushedUsage
}

func (s *fakeSink) UsageDue(time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.due
}

func (s *fakeSink) AcceptUsage(u PushedUsage) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.HasPrefix(string(u.Body), "{") {
		return false, errors.New("not a usage response")
	}
	s.received = append(s.received, u)
	return true, nil
}

func TestUsagePush(t *testing.T) {
	sink := &fakeSink{}
	rc := NewReceiver(t.TempDir(), []string{"tok"}, &memOffsets{})
	rc.SetUsageSink(sink)
	srv := httptest.NewServer(rc)
	defer srv.Close()

	a := &Agent{ServerURL: srv.URL, Token: "tok", Machine: "laptop"}
	fetches := 0
	fetch := func(context.Context) ([]byte, string, string, error) {
		fetches++
		return []byte(`{"five_hour":{"utilization":12}}`), "team", "max_5x", nil
	}
	ctx := context.Background()

	// Not due: the usage API is not called.
	if pushed, err := a.syncUsage(ctx, fetch); err != nil || pushed || fetches != 0 {
		t.Fatalf("not due: pushed=%v err=%v fetches=%d", pushed, err, fetches)
	}

	sink.due = true
	before := time.Now()
	if pushed, err := a.syncUsage(ctx, fetch); err != nil || !pushed || fetches != 1 {
		t.Fatalf("due: pushed=%v err=%v fetches=%d", pushed, err, fetches)
	}
	got := sink.received[0]
	if got.Machine != "laptop" || got.SubscriptionType != "team" || got.RateLimitTier != "max_5x" ||
		string(got.Body) != `{"five_hour":{"utilization":12}}` || got.FetchedAt.Before(before.Add(-time.Second)) {
		t.Errorf("received %+v", got)
	}

	// A machine without credentials reports it and sends nothing.
	noCreds := func(context.Context) ([]byte, string, string, error) { return nil, "", "", ErrNoCredentials }
	if _, err := a.syncUsage(ctx, noCreds); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("no credentials: err = %v", err)
	}
	if len(sink.received) != 1 {
		t.Errorf("received %d readings, want 1", len(sink.received))
	}
}

func TestUsageRoutes(t *testing.T) {
	rc := NewReceiver(t.TempDir(), []string{"tok"}, &memOffsets{})
	do := func(method, url, token string, body string) int {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set(HeaderMachine, "laptop")
		req.Header.Set(HeaderFetchedAt, "1700000000000000000")
		w := httptest.NewRecorder()
		rc.ServeHTTP(w, req)
		return w.Code
	}
	if code := do("GET", "/api/ingest?kind=usage", "tok", ""); code != http.StatusNotFound {
		t.Errorf("usage without sink: %d, want 404", code)
	}
	rc.SetUsageSink(&fakeSink{})
	for _, tt := range []struct {
		name, method, url, token, body string
		want                           int
	}{
		{"due check", "GET", "/api/ingest?kind=usage", "tok", "", http.StatusOK},
		{"due check without token", "GET", "/api/ingest?kind=usage", "", "", http.StatusUnauthorized},
		{"reading", "POST", "/api/ingest?kind=usage", "tok", `{"x":1}`, http.StatusOK},
		{"invalid reading", "POST", "/api/ingest?kind=usage", "tok", "garbage", http.StatusBadRequest},
		{"other method", "DELETE", "/api/ingest?kind=usage", "tok", "", http.StatusMethodNotAllowed},
		{"GET on the file route", "GET", "/api/ingest", "tok", "", http.StatusMethodNotAllowed},
	} {
		if code := do(tt.method, tt.url, tt.token, tt.body); code != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, code, tt.want)
		}
	}
}

// trackerLog records the contacts reported by a receiver.
type trackerLog struct {
	mu       sync.Mutex
	contacts []string
}

func (l *trackerLog) MachineSeen(machine string, _ time.Time, gotData bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.contacts = append(l.contacts, machine+"|"+map[bool]string{true: "data", false: "seen"}[gotData])
}

func TestReceiverReportsContacts(t *testing.T) {
	log := &trackerLog{}
	rc := NewReceiver(t.TempDir(), []string{"tok"}, &memOffsets{})
	rc.SetUsageSink(&fakeSink{})
	rc.SetMachineTracker(log)

	send(t, rc, chunkReq{token: "tok", machine: "laptop", path: "projects/-p/s.jsonl", end: 2, body: "a\n"})
	send(t, rc, chunkReq{token: "tok", machine: "laptop", path: "projects/-p/s.jsonl", offset: 2, end: 2}) // empty check
	send(t, rc, chunkReq{token: "bad", machine: "intruder", path: "projects/-p/s.jsonl", end: 2, body: "a\n"})
	req := httptest.NewRequest("GET", "/api/ingest?kind=usage", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set(HeaderMachine, "home")
	rc.ServeHTTP(httptest.NewRecorder(), req)

	want := "laptop|data,laptop|seen,home|seen"
	if got := strings.Join(log.contacts, ","); got != want {
		t.Errorf("contacts = %s, want %s", got, want)
	}
}
