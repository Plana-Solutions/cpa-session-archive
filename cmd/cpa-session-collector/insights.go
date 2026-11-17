package main

import (
	"net/http"
	"strconv"
	"time"

	"cpa-session-archive/internal/archive"
)

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

	// Serialize cache misses so concurrent page loads do not rescan the slow volume.
	s.insightsMu.Lock()
	defer s.insightsMu.Unlock()
	now := time.Now().UTC()
	cached, found := s.insightsCache[days]
	generated, _ := time.Parse(time.RFC3339Nano, cached.GeneratedAt)
	if !found || now.Sub(generated) >= 5*time.Minute || generated.UTC().Day() != now.Day() {
		cached, err = s.s.Insights(r.Context(), days, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.insightsCache == nil {
			s.insightsCache = map[int]archive.Insights{}
		}
		s.insightsCache[days] = cached
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
