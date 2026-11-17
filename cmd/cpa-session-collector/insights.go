package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"cpa-session-archive/internal/archive"
)

const insightsTTL = 5 * time.Minute

type insightsEntry struct {
	value    archive.Insights
	ready    bool
	building bool
}

// A cold scan of turn_records takes minutes on the production volume while the
// plugin proxy times out after seconds, so requests never wait on a scan: they
// get the cached result (possibly stale) or a building status to poll.
func (s *server) insights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		days = 30
	}
	days = max(1, min(days, 365))
	minimum, err := strconv.ParseInt(r.URL.Query().Get("min_requests"), 10, 64)
	if err != nil {
		minimum = 20
	}
	minimum = max(0, minimum)

	cached, ready := s.cachedInsights(days, time.Now().UTC())
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"status": "building"})
		return
	}
	out := cached
	out.Models = []archive.ModelInsight{}
	for _, model := range cached.Models {
		if model.Requests >= minimum {
			out.Models = append(out.Models, model)
		}
	}
	writeJSON(w, out)
}

func (s *server) cachedInsights(days int, now time.Time) (archive.Insights, bool) {
	s.insightsMu.Lock()
	defer s.insightsMu.Unlock()
	if s.insightsCache == nil {
		s.insightsCache = map[int]*insightsEntry{}
	}
	entry := s.insightsCache[days]
	if entry == nil {
		entry = &insightsEntry{}
		s.insightsCache[days] = entry
	}
	generated, _ := time.Parse(time.RFC3339Nano, entry.value.GeneratedAt)
	stale := !entry.ready || now.Sub(generated) >= insightsTTL || generated.UTC().YearDay() != now.YearDay()
	if stale && !entry.building {
		entry.building = true
		go s.buildInsights(days, entry)
	}
	return entry.value, entry.ready
}

func (s *server) buildInsights(days int, entry *insightsEntry) {
	value, err := s.s.Insights(context.Background(), days, time.Now().UTC())
	s.insightsMu.Lock()
	defer s.insightsMu.Unlock()
	entry.building = false
	if err != nil {
		log.Printf("insights %dd: %v", days, err)
		return
	}
	entry.value, entry.ready = value, true
}
