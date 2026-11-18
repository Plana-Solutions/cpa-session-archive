package archive

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestClickHouseScale(t *testing.T) {
	if os.Getenv("ARCHIVE_SCALE_URL") == "" {
		t.Skip("scale fixture opt-in required")
	}
	s := &summaryTransport{base: scaleTransport{}}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("ARCHIVE_SCALE_URL"), Database: "cliproxy", User: "default"}, client: &http.Client{Transport: s, Timeout: 60 * time.Second}}
	cases := []struct {
		name, endpoint string
		q              url.Values
	}{
		{"list", "sessions", url.Values{}},
		{"rare", "sessions", url.Values{"q": {"rarelyfoundneedle"}}},
		{"common", "sessions", url.Values{"q": {"common"}}},
		{"turns", "turns", url.Values{"session_id": {"claude:scale-24990"}}},
		{"steps", "turns", url.Values{"session_id": {"claude:scale-24990"}, "turn_id": {"req-299880"}}},
		{"request", "requests", url.Values{"id": {"req-299842"}}},
		{"insights30", "insights", url.Values{"days": {"30"}}},
		{"insights90", "insights", url.Values{"days": {"90"}}},
	}
	for _, v := range cases {
		v.q.Set("to", "2026-11-18T16:00:01Z")
		// A fixed from makes the fixture reproducible after the calendar advances.
		days := 365
		if v.name == "rare" || v.name == "common" || v.name == "insights30" {
			days = 30
		}
		if v.name == "insights90" {
			days = 90
		}
		end := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
		v.q.Set("from", end.AddDate(0, 0, 1-days).Format(time.RFC3339))
		s.rows = 0
		s.bytes = 0
		start := time.Now()
		out, err := c.read(v.endpoint, v.q)
		elapsed := time.Since(start).Seconds()
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		b, _ := json.Marshal(out)
		if len(b) < 3 || string(b) == "[]" {
			t.Fatalf("%s empty result", v.name)
		}
		switch v.name {
		case "list", "common", "rare":
			rows := out.([]archiveSession)
			want := 20
			if v.name == "rare" {
				want = 1
			}
			if len(rows) != want || rows[0].Requests != 12 || rows[0].TotalTokens != 13200 {
				t.Fatalf("%s unexpected sessions %+v", v.name, rows)
			}
		case "turns":
			if out.(map[string]any)["total"] != int64(1) {
				t.Fatal("turn count changed")
			}
		case "steps":
			if out.(map[string]any)["total"] != 12 {
				t.Fatal("step count changed")
			}
		case "request":
			if len(out.(Record).OriginalRequest) != 256012 {
				t.Fatal("large request body lost")
			}
		case "insights30":
			if out.(Insights).Totals.Requests != 198444 {
				t.Fatalf("30 day count %d", out.(Insights).Totals.Requests)
			}
		case "insights90":
			if out.(Insights).Totals.Requests != 300000 {
				t.Fatal("90 day count changed")
			}
		}

		if v.name == "list" || v.name == "turns" || v.name == "steps" {
			if s.bytes >= 50_000_000 || elapsed >= 1 {
				t.Fatalf("%s exceeds 50 MB / 1 s target: bytes=%d seconds=%f", v.name, s.bytes, elapsed)
			}
		}
		t.Logf("SCALE name=%s rows=%d bytes=%d seconds=%.6f result_bytes=%d", v.name, s.rows, s.bytes, elapsed, len(b))
	}
}

type scaleTransport struct{}

func (scaleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	q := r.URL.Query()
	q.Set("use_query_condition_cache", "0")
	q.Set("use_query_cache", "0")
	if os.Getenv("ARCHIVE_SCALE_IGNORE_INDEX") != "" {
		q.Set("ignore_data_skipping_indices", "archive_user_text_ngram")
	}
	r.URL.RawQuery = q.Encode()
	return http.DefaultTransport.RoundTrip(r)
}
