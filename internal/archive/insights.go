package archive

import (
	"context"
	"sort"
	"strings"
	"time"
)

type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGoogle    Provider = "google"
	ProviderOther     Provider = "other"
)

var providerPrefixes = []struct {
	prefix   string
	provider Provider
}{
	{"claude-", ProviderAnthropic},
	{"gpt-", ProviderOpenAI},
	{"o1", ProviderOpenAI},
	{"o3", ProviderOpenAI},
	{"o4", ProviderOpenAI},
	{"codex-", ProviderOpenAI},
	{"gemini-", ProviderGoogle},
}

func modelProvider(model string) Provider {
	for _, entry := range providerPrefixes {
		if strings.HasPrefix(model, entry.prefix) {
			return entry.provider
		}
	}
	return ProviderOther
}

type Insights struct {
	WindowDays  int            `json:"window_days"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	GeneratedAt string         `json:"generated_at"`
	Totals      InsightTotals  `json:"totals"`
	Models      []ModelInsight `json:"models"`
	Daily       []DailyInsight `json:"daily"`
}

type InsightTotals struct {
	Requests int64 `json:"requests"`
	Sessions int64 `json:"sessions"`
	Turns    int64 `json:"turns"`
	Models   int64 `json:"models"`
}

type ModelInsight struct {
	Model             string   `json:"model"`
	Provider          Provider `json:"provider"`
	Requests          int64    `json:"requests"`
	Sessions          int64    `json:"sessions"`
	Turns             int64    `json:"turns"`
	StepsPerTurn      float64  `json:"steps_per_turn"`
	AvgLatencySeconds float64  `json:"avg_latency_seconds"`
	FailureRate       float64  `json:"failure_rate"`
	CancelRate        float64  `json:"cancel_rate"`
}

type DailyInsight struct {
	Day      string             `json:"day"`
	Requests map[Provider]int64 `json:"requests"`
}

type turnKey [2]string

type modelAggregate struct {
	ModelInsight
	sessions                    map[string]struct{}
	turns                       map[turnKey]struct{}
	latency                     float64
	succeeded, failed, canceled int64
}

type windowAggregate struct {
	out      Insights
	from     time.Time
	models   map[string]*modelAggregate
	sessions map[string]struct{}
	turns    map[turnKey]struct{}
}

func newWindowAggregate(days int, today, now time.Time) *windowAggregate {
	from := today.AddDate(0, 0, 1-days)
	w := &windowAggregate{
		out:    Insights{WindowDays: days, From: from.Format(time.RFC3339), To: now.Format(time.RFC3339Nano), GeneratedAt: now.Format(time.RFC3339Nano), Models: []ModelInsight{}, Daily: make([]DailyInsight, days)},
		from:   from,
		models: map[string]*modelAggregate{}, sessions: map[string]struct{}{}, turns: map[turnKey]struct{}{},
	}
	for i := range w.out.Daily {
		w.out.Daily[i] = DailyInsight{Day: from.AddDate(0, 0, i).Format(time.DateOnly), Requests: map[Provider]int64{ProviderAnthropic: 0, ProviderOpenAI: 0, ProviderGoogle: 0, ProviderOther: 0}}
	}
	return w
}

func (w *windowAggregate) add(model, session, turn, outcome string, start, end time.Time, ended bool) {
	m := w.models[model]
	if m == nil {
		m = &modelAggregate{ModelInsight: ModelInsight{Model: model, Provider: modelProvider(model)}, sessions: map[string]struct{}{}, turns: map[turnKey]struct{}{}}
		w.models[model] = m
	}
	m.Requests++
	w.out.Totals.Requests++
	m.sessions[session] = struct{}{}
	w.sessions[session] = struct{}{}
	if turn != "" {
		key := turnKey{session, turn}
		m.turns[key] = struct{}{}
		w.turns[key] = struct{}{}
	}
	switch outcome {
	case "succeeded":
		if ended && !end.Before(start) {
			m.latency += end.Sub(start).Seconds()
			m.succeeded++
		}
	case "failed", "rejected":
		m.failed++
	case "canceled":
		m.canceled++
	}
	w.out.Daily[int(start.Sub(w.from)/(24*time.Hour))].Requests[m.Provider]++
}

func (w *windowAggregate) finish() Insights {
	out := w.out
	out.Totals.Sessions, out.Totals.Turns, out.Totals.Models = int64(len(w.sessions)), int64(len(w.turns)), int64(len(w.models))
	for _, m := range w.models {
		m.Sessions, m.Turns = int64(len(m.sessions)), int64(len(m.turns))
		if m.Turns > 0 {
			m.StepsPerTurn = float64(m.Requests) / float64(m.Turns)
		}
		if m.succeeded > 0 {
			m.AvgLatencySeconds = m.latency / float64(m.succeeded)
		}
		m.FailureRate = float64(m.failed) / float64(m.Requests)
		m.CancelRate = float64(m.canceled) / float64(m.Requests)
		out.Models = append(out.Models, m.ModelInsight)
	}
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Requests == out.Models[j].Requests {
			return out.Models[i].Model < out.Models[j].Model
		}
		return out.Models[i].Requests > out.Models[j].Requests
	})
	return out
}

// Insights aggregates every requested window (in days, today in UTC included)
// from one pass over the narrow projection. The scan cost is dominated by the
// table walk, so extra windows are nearly free.
func (s *Store) Insights(ctx context.Context, windows []int, now time.Time) (map[int]Insights, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	aggs := map[int]*windowAggregate{}
	widest := today
	for _, days := range windows {
		days = max(1, min(days, 365))
		if aggs[days] == nil {
			aggs[days] = newWindowAggregate(days, today, now)
			if aggs[days].from.Before(widest) {
				widest = aggs[days].from
			}
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT COALESCE(NULLIF(model,''),requested_model), session_id, COALESCE(turn_id,''), COALESCE(outcome,''), started_at, COALESCE(completed_at,'')
		FROM turn_records
		WHERE COALESCE(NULLIF(model,''),requested_model,'') != '' AND COALESCE(outcome,'') != 'imported'
		AND julianday(started_at) >= julianday(?) AND julianday(started_at) <= julianday(?)`, widest.Format(time.RFC3339), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var model, session, turn, outcome, started, completed string
		if err := rows.Scan(&model, &session, &turn, &outcome, &started, &completed); err != nil {
			return nil, err
		}
		start, err := time.Parse(time.RFC3339Nano, started)
		if err != nil || start.After(now) {
			continue
		}
		start = start.UTC()
		end, endErr := time.Parse(time.RFC3339Nano, completed)
		for _, w := range aggs {
			if !start.Before(w.from) {
				w.add(model, session, turn, outcome, start, end, endErr == nil)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[int]Insights, len(aggs))
	for days, w := range aggs {
		out[days] = w.finish()
	}
	return out, nil
}
