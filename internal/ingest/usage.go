package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Usage gauges (multi-machine mode). The usage API needs Claude credentials,
// which only the machines running Claude Code keep fresh, so agents fetch
// the usage for the server. The server decides when a new reading is
// wanted; any agent with valid credentials provides it.
//
//	GET  /api/ingest?kind=usage   -> {"due": bool}: should this agent fetch now?
//	                                 (claumon agent asks every 30 seconds)
//	POST /api/ingest?kind=usage   body: the usage API response, as is
//	     X-Claumon-Machine, X-Claumon-Fetched-At (unix nanoseconds),
//	     X-Claumon-Subscription, X-Claumon-Rate-Limit-Tier
//	                              -> {"accepted": bool}
//
// Both carry the agent's bearer token, like file uploads.

// Headers of usage pushes.
const (
	HeaderFetchedAt    = "X-Claumon-Fetched-At"
	HeaderSubscription = "X-Claumon-Subscription"
	HeaderTier         = "X-Claumon-Rate-Limit-Tier"
)

// maxUsageBytes bounds a usage API response.
const maxUsageBytes = 1 << 20

// PushedUsage is a usage reading sent by an agent.
type PushedUsage struct {
	Machine          string
	Body             []byte // the usage API response
	FetchedAt        time.Time
	SubscriptionType string
	RateLimitTier    string
}

// UsageSink is the server side of usage pushes.
type UsageSink interface {
	// UsageDue reports whether an agent asking now should fetch a reading.
	UsageDue(now time.Time) bool
	// AcceptUsage records a reading; false when it is older than the last.
	AcceptUsage(u PushedUsage) (bool, error)
}

// SetUsageSink enables usage pushes on the receiver.
func (rc *Receiver) SetUsageSink(s UsageSink) {
	rc.usage = s
}

// serveUsage handles GET and POST /api/ingest?kind=usage.
func (rc *Receiver) serveUsage(w http.ResponseWriter, r *http.Request) {
	if rc.usage == nil {
		http.Error(w, "usage pushes are not enabled", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(map[string]bool{"due": rc.usage.UsageDue(time.Now())})
	case http.MethodPost:
		machine := r.Header.Get(HeaderMachine)
		ns, err := strconv.ParseInt(r.Header.Get(HeaderFetchedAt), 10, 64)
		if !ValidMachineName(machine) || err != nil || ns <= 0 {
			http.Error(w, "invalid machine or fetch time", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxUsageBytes+1))
		if err != nil || len(body) > maxUsageBytes {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		accepted, err := rc.usage.AcceptUsage(PushedUsage{
			Machine:          machine,
			Body:             body,
			FetchedAt:        time.Unix(0, ns),
			SubscriptionType: r.Header.Get(HeaderSubscription),
			RateLimitTier:    r.Header.Get(HeaderTier),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"accepted": accepted})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// UsageFetcher fetches a reading from the usage API with the machine's
// credentials. ErrNoCredentials means this machine cannot fetch now.
type UsageFetcher func(ctx context.Context) (body []byte, subscription, tier string, err error)

// ErrNoCredentials is returned by a UsageFetcher without valid credentials.
var ErrNoCredentials = errors.New("no valid Claude credentials on this machine")

// RunUsage asks the server every interval whether a usage reading is due
// and, when it is, fetches one and sends it, until ctx is done. Problems are
// logged when they change, not at every interval.
func (a *Agent) RunUsage(ctx context.Context, fetch UsageFetcher, interval time.Duration, logf func(format string, args ...any)) {
	lastProblem := ""
	report := func(problem string) {
		if problem != lastProblem && problem != "" {
			logf("[agent] Usage: %s", problem)
		}
		lastProblem = problem
	}
	for {
		pushed, err := a.syncUsage(ctx, fetch)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			report(err.Error())
		case pushed:
			logf("[agent] Usage reading sent")
			report("")
		default:
			report("")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// syncUsage asks whether a reading is due and sends one when it is.
func (a *Agent) syncUsage(ctx context.Context, fetch UsageFetcher) (bool, error) {
	var due struct {
		Due bool `json:"due"`
	}
	if err := a.usageRequest(ctx, http.MethodGet, nil, nil, &due); err != nil {
		return false, err
	}
	if !due.Due {
		return false, nil
	}
	fetchedAt := time.Now()
	body, sub, tier, err := fetch(ctx)
	if err != nil {
		return false, err
	}
	headers := map[string]string{
		HeaderMachine:      a.Machine,
		HeaderFetchedAt:    strconv.FormatInt(fetchedAt.UnixNano(), 10),
		HeaderSubscription: sub,
		HeaderTier:         tier,
	}
	var reply struct {
		Accepted bool `json:"accepted"`
	}
	if err := a.usageRequest(ctx, http.MethodPost, body, headers, &reply); err != nil {
		return false, err
	}
	return reply.Accepted, nil
}

func (a *Agent) usageRequest(ctx context.Context, method string, body []byte, headers map[string]string, out any) error {
	url := strings.TrimSuffix(a.ServerURL, "/") + "/api/ingest?kind=usage"
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server replied %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
