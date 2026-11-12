package server

import (
	"net/http"
	"strconv"
	"time"
)

// The plan's limits are only known as percentages (the usage API gives the
// utilization of the 5-hour session and weekly limits, not their size), so
// the limits chart shows the stored readings over time and the peak each
// limit window reached.

type limitReading struct {
	Time    time.Time `json:"t"`
	Session float64   `json:"session"`
	Weekly  float64   `json:"weekly"`
}

// limitWindow is one limit window (identified by its reset time) and the
// highest utilization read during it.
type limitWindow struct {
	ResetAt string    `json:"reset_at"`
	Peak    float64   `json:"peak"`
	PeakAt  time.Time `json:"peak_at"`
}

type limitsReport struct {
	Readings       []limitReading `json:"readings"`
	WeeklyWindows  []limitWindow  `json:"weekly_windows"`
	SessionWindows []limitWindow  `json:"session_windows"`
}

// peaks returns the peak of each window, in order of first reading.
func peaks(readings []limitReading, resets []string, pct func(limitReading) float64) []limitWindow {
	out := []limitWindow{}
	index := map[string]int{}
	for i, r := range readings {
		reset := resets[i]
		if reset == "" {
			continue
		}
		j, ok := index[reset]
		if !ok {
			out = append(out, limitWindow{ResetAt: reset, Peak: -1})
			j = len(out) - 1
			index[reset] = j
		}
		if p := pct(r); p > out[j].Peak {
			out[j].Peak, out[j].PeakAt = p, r.Time
		}
	}
	return out
}

// HandleLimits returns the usage readings of the last `days` days (default
// 7, at most the history window) and the peak of each weekly and session
// window among them. The limits are the Claude account's, shared by all
// machines.
func (h *Handlers) HandleLimits(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			days = min(n, h.historyMaxDays())
		}
	}
	stored, err := h.store.UsageReadings(time.Now().AddDate(0, 0, -days))
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	readings := make([]limitReading, 0, len(stored))
	sessionResets := make([]string, 0, len(stored))
	weeklyResets := make([]string, 0, len(stored))
	for _, s := range stored {
		readings = append(readings, limitReading{Time: s.Time, Session: s.SessionPct, Weekly: s.WeeklyPct})
		sessionResets = append(sessionResets, s.SessionResetAt)
		weeklyResets = append(weeklyResets, s.WeeklyResetAt)
	}
	writeJSON(w, limitsReport{
		Readings:       readings,
		WeeklyWindows:  peaks(readings, weeklyResets, func(r limitReading) float64 { return r.Weekly }),
		SessionWindows: peaks(readings, sessionResets, func(r limitReading) float64 { return r.Session }),
	})
}
