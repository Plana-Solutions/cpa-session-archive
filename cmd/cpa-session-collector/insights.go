package main

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"cpa-session-archive/internal/archive"
)

const insightsTTL = 5 * time.Minute

var insightsWindows = []int{7, 30, 90}

type insightsSnapshot struct {
	generated time.Time
	windows   map[int]archive.Insights
}

// A cold scan of turn_records takes minutes on the production volume while the
// plugin proxy times out after seconds, so requests never wait on a scan. One
// scan builds every window; requests get the current snapshot or a building
// status to poll.
func (s *server) insights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || !slices.Contains(insightsWindows, parsed) {
			http.Error(w, "days must be 7, 30 or 90", http.StatusBadRequest)
			return
		}
		days = parsed
	}
	minimum, err := strconv.ParseInt(r.URL.Query().Get("min_requests"), 10, 64)
	if err != nil {
		minimum = 20
	}
	minimum = max(0, minimum)

	snapshot, ready := s.currentInsights(time.Now().UTC())
	if !ready {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"status": "building"})
		return
	}
	out := snapshot.windows[days]
	cached := out.Models
	out.Models = []archive.ModelInsight{}
	for _, model := range cached {
		if model.Requests >= minimum {
			out.Models = append(out.Models, model)
		}
	}
	writeJSON(w, out)
}

func (s *server) currentInsights(now time.Time) (insightsSnapshot, bool) {
	s.insightsMu.Lock()
	defer s.insightsMu.Unlock()
	snap := s.insightsSnapshot
	// A snapshot from another UTC day or from the future covers the wrong
	// windows, so it is withheld rather than served stale.
	servable := snap.windows != nil && !snap.generated.After(now) && snap.generated.Format(time.DateOnly) == now.Format(time.DateOnly)
	if (!servable || now.Sub(snap.generated) >= insightsTTL) && !s.insightsScanning {
		s.insightsScanning = true
		go s.buildInsights()
	}
	return snap, servable
}

func (s *server) buildInsights() {
	now := time.Now().UTC()
	windows, err := s.s.Insights(context.Background(), insightsWindows, now)
	s.insightsMu.Lock()
	defer s.insightsMu.Unlock()
	s.insightsScanning = false
	if err != nil {
		log.Printf("insights: %v", err)
		return
	}
	s.insightsSnapshot = insightsSnapshot{generated: now, windows: windows}
}
