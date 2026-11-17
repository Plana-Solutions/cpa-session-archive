package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestInsightsBuildInBackgroundThenServeCache(t *testing.T) {
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
	call := func(method, query string) (int, map[string]any, archive.Insights) {
		t.Helper()
		w := httptest.NewRecorder()
		s.insights(w, httptest.NewRequest(method, "/v1/insights"+query, nil))
		var raw map[string]any
		var out archive.Insights
		_ = json.Unmarshal(w.Body.Bytes(), &raw)
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, raw, out
	}
	ready := func(query string) archive.Insights {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			code, _, out := call("GET", query)
			if code == 200 {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("insights%s never became ready", query)
		return archive.Insights{}
	}

	if code, raw, _ := call("GET", ""); code != 202 || raw["status"] != "building" {
		t.Fatalf("cold request = %d %v, want 202 building", code, raw)
	}
	first := ready("")
	if first.WindowDays != 30 || len(first.Models) != 0 || first.Totals.Requests != 1 {
		t.Fatalf("default = %+v", first)
	}
	if _, err := store.DB.Exec(`DELETE FROM turn_records`); err != nil {
		t.Fatal(err)
	}
	if out := ready("?days=30&min_requests=1"); len(out.Models) != 1 || out.GeneratedAt != first.GeneratedAt {
		t.Fatalf("fresh cache not reused: %+v", out)
	}
	if out := ready("?days=30&min_requests=2"); len(out.Models) != 0 || out.Totals.Models != 1 {
		t.Fatalf("threshold = %+v", out)
	}

	s.insightsMu.Lock()
	s.insightsCache[30].value.GeneratedAt = time.Now().Add(-6 * time.Minute).Format(time.RFC3339Nano)
	s.insightsMu.Unlock()
	if code, _, out := call("GET", "?days=30"); code != 200 || out.Totals.Requests != 1 {
		t.Fatalf("stale cache should be served while rebuilding: %d %+v", code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, out := call("GET", "?days=30"); out.Totals.Requests == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale cache never refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if out := ready("?days=0"); out.WindowDays != 1 || len(out.Daily) != 1 {
		t.Fatalf("lower clamp = %+v", out)
	}
	if out := ready("?days=999"); out.WindowDays != 365 || len(out.Daily) != 365 {
		t.Fatalf("upper clamp = %+v", out)
	}
	if code, _, _ := call("POST", ""); code != 405 {
		t.Fatalf("POST status %d", code)
	}
}
