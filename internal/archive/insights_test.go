package archive

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestInsightsWindowAndMetrics(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	rows := []struct{ model, requested, session, turn, outcome, start, end string }{
		{"claude-opus", "", "s1", "t1", "succeeded", "2026-11-18T04:32:25.466131827+08:00", "2026-11-18T04:32:27.966131827+08:00"},
		{"claude-opus", "", "s1", "t1", "succeeded", "2026-11-17T21:00:00Z", "2026-11-17T21:00:07.5Z"},
		{"claude-opus", "", "s2", "t2", "failed", "2026-11-18T00:00:00Z", "2026-11-18T00:01:00Z"},
		{"claude-opus", "", "s2", "t2", "rejected", "2026-11-18T01:00:00Z", ""},
		{"claude-opus", "", "s2", "", "canceled", "2026-11-18T02:00:00Z", ""},
		{"", "gpt-test", "s1", "t1", "succeeded", "2026-11-18T03:00:00Z", ""},
		{"gemini-test", "", "s3", "t3", "succeeded", "2026-11-18T04:00:00Z", "2026-11-18T04:00:10Z"},
		{"", "", "external", "ext", "imported", "2026-11-18T00:00:00Z", ""},
		{"gpt-import", "", "external", "ext", "imported", "2026-11-18T00:00:00Z", ""},
		{"gpt-old", "", "old", "old", "failed", "2026-11-15T23:59:59Z", ""},
		{"gpt-future", "", "future", "future", "failed", "2026-11-19T00:00:00Z", ""},
	}
	for i, row := range rows {
		_, err := store.DB.Exec(`INSERT INTO turn_records(request_id,model,requested_model,session_id,turn_id,outcome,started_at,completed_at) VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprint(i), row.model, row.requested, row.session, row.turn, row.outcome, row.start, row.end)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	got, err := store.Insights(context.Background(), 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals != (InsightTotals{Requests: 7, Sessions: 3, Turns: 3, Models: 3}) {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if got.From != "2026-11-16T00:00:00Z" || got.To != "2026-11-18T12:00:00Z" || got.GeneratedAt != got.To {
		t.Fatalf("window = %+v", got)
	}
	if len(got.Models) != 3 {
		t.Fatalf("models = %+v", got.Models)
	}
	claude := got.Models[0]
	if claude.Model != "claude-opus" || claude.Provider != ProviderAnthropic || claude.Requests != 5 || claude.Sessions != 2 || claude.Turns != 2 {
		t.Fatalf("claude = %+v", claude)
	}
	if claude.StepsPerTurn != 2.5 || math.Abs(claude.AvgLatencySeconds-5) > 1e-9 || claude.FailureRate != .4 || claude.CancelRate != .2 {
		t.Fatalf("metrics = %+v", claude)
	}
	if got.Models[1].Model != "gemini-test" || got.Models[2].Model != "gpt-test" || got.Models[2].AvgLatencySeconds != 0 {
		t.Fatalf("models = %+v", got.Models)
	}
	if len(got.Daily) != 3 || got.Daily[0].Day != "2026-11-16" || got.Daily[0].Requests[ProviderAnthropic] != 0 || len(got.Daily[0].Requests) != 4 {
		t.Fatalf("zero day = %+v", got.Daily)
	}
	if got.Daily[1].Day != "2026-11-17" || got.Daily[1].Requests[ProviderAnthropic] != 2 || got.Daily[2].Requests[ProviderAnthropic] != 3 {
		t.Fatalf("UTC buckets = %+v", got.Daily)
	}
}

func TestModelProviderRegistry(t *testing.T) {
	for model, want := range map[string]Provider{"claude-test": ProviderAnthropic, "gpt-test": ProviderOpenAI, "o1-mini": ProviderOpenAI, "o3": ProviderOpenAI, "o4-mini": ProviderOpenAI, "codex-test": ProviderOpenAI, "gemini-test": ProviderGoogle, "kimi": ProviderOther} {
		if got := modelProvider(model); got != want {
			t.Errorf("%s = %s, want %s", model, got, want)
		}
	}
}
