package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestInsightsCacheAndThreshold(t *testing.T) {
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	_, err = store.DB.Exec(`INSERT INTO turn_records(request_id,session_id,model,started_at,outcome) VALUES('r','s','gpt-test',?,'failed')`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{s: store}
	get := func(query string) archive.Insights {
		t.Helper()
		w := httptest.NewRecorder()
		s.insights(w, httptest.NewRequest("GET", "/v1/insights"+query, nil))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var out archive.Insights
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := get("")
	if first.WindowDays != 30 || len(first.Models) != 0 || first.Totals.Requests != 1 {
		t.Fatalf("default = %+v", first)
	}
	if _, err := store.DB.Exec(`DELETE FROM turn_records`); err != nil {
		t.Fatal(err)
	}
	cached := get("?days=30&min_requests=1")
	if len(cached.Models) != 1 || cached.Totals.Requests != 1 || cached.GeneratedAt != first.GeneratedAt {
		t.Fatalf("cache = %+v", cached)
	}
	if out := get("?days=30&min_requests=2"); len(out.Models) != 0 || out.Totals.Models != 1 {
		t.Fatalf("threshold = %+v", out)
	}
	expired := s.insightsCache[30]
	expired.GeneratedAt = time.Now().Add(-6 * time.Minute).Format(time.RFC3339Nano)
	s.insightsCache[30] = expired
	if out := get("?days=30&min_requests=0"); out.Totals.Requests != 0 {
		t.Fatalf("expired cache = %+v", out)
	}
	if out := get("?days=0"); out.WindowDays != 1 || len(out.Daily) != 1 {
		t.Fatalf("lower clamp = %+v", out)
	}
	if out := get("?days=999"); out.WindowDays != 365 || len(out.Daily) != 365 {
		t.Fatalf("upper clamp = %+v", out)
	}
	w := httptest.NewRecorder()
	s.insights(w, httptest.NewRequest("POST", "/v1/insights", nil))
	if w.Code != 405 {
		t.Fatalf("POST status %d", w.Code)
	}
}
