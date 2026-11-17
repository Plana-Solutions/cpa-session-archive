package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestInsightsBuildOnceThenServeEveryWindow(t *testing.T) {
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
	call := func(method, query string) (int, archive.Insights) {
		t.Helper()
		w := httptest.NewRecorder()
		s.insights(w, httptest.NewRequest(method, "/v1/insights"+query, nil))
		var out archive.Insights
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	ready := func(query string) archive.Insights {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if code, out := call("GET", query); code == 200 {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("insights%s never became ready", query)
		return archive.Insights{}
	}
	scanning := func() bool {
		s.insightsMu.Lock()
		defer s.insightsMu.Unlock()
		return s.insightsScanning
	}

	cold := httptest.NewRecorder()
	s.insights(cold, httptest.NewRequest("GET", "/v1/insights", nil))
	sent := cold.Result()
	if sent.StatusCode != 202 || sent.Header.Get("Content-Type") != "application/json" || !strings.Contains(cold.Body.String(), `"building"`) {
		t.Fatalf("cold request = %d %q %s, want 202 JSON building", sent.StatusCode, sent.Header.Get("Content-Type"), cold.Body.String())
	}
	first := ready("")
	if first.WindowDays != 30 || len(first.Models) != 0 || first.Totals.Requests != 1 {
		t.Fatalf("default = %+v", first)
	}
	for _, days := range []string{"7", "90"} {
		code, out := call("GET", "?days="+days)
		if code != 200 || out.Totals.Requests != 1 || out.GeneratedAt != first.GeneratedAt || scanning() {
			t.Fatalf("days=%s must come from the same scan without a new one: %d %+v", days, code, out)
		}
	}

	if _, err := store.DB.Exec(`DELETE FROM turn_records`); err != nil {
		t.Fatal(err)
	}
	if code, out := call("GET", "?days=30&min_requests=1"); code != 200 || len(out.Models) != 1 || out.GeneratedAt != first.GeneratedAt {
		t.Fatalf("fresh snapshot not reused: %+v", out)
	}
	if _, out := call("GET", "?days=30&min_requests=2"); len(out.Models) != 0 || out.Totals.Models != 1 {
		t.Fatalf("threshold = %+v", out)
	}

	s.insightsMu.Lock()
	s.insightsSnapshot.generated = time.Now().UTC().Add(-6 * time.Minute)
	s.insightsMu.Unlock()
	if code, out := call("GET", "?days=30"); code != 200 || out.Totals.Requests != 1 {
		t.Fatalf("stale snapshot should be served while rebuilding: %d %+v", code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, out := call("GET", "?days=30"); out.Totals.Requests == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale snapshot never refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.insightsMu.Lock()
	s.insightsSnapshot.generated = time.Now().UTC().AddDate(0, 0, -1)
	s.insightsMu.Unlock()
	if code, _ := call("GET", "?days=30"); code != 202 {
		t.Fatalf("yesterday's snapshot must not be served: %d", code)
	}
	ready("?days=30")

	for _, query := range []string{"?days=0", "?days=14", "?days=abc"} {
		if code, _ := call("GET", query); code != 400 {
			t.Fatalf("%s status %d, want 400", query, code)
		}
	}
	if code, _ := call("POST", ""); code != 405 {
		t.Fatalf("POST status %d", code)
	}
}
