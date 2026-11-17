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

type modelAggregate struct {
	ModelInsight
	sessions                    map[string]struct{}
	turns                       map[string]struct{}
	latency                     float64
	succeeded, failed, canceled int64
}

// Insights reads only the narrow projection. The window includes today in UTC.
func (s *Store) Insights(ctx context.Context, days int, now time.Time) (Insights, error) {
	days = max(1, min(days, 365))
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, 1-days)
	out := Insights{WindowDays: days, From: from.Format(time.RFC3339), To: now.Format(time.RFC3339Nano), GeneratedAt: now.Format(time.RFC3339Nano), Models: []ModelInsight{}, Daily: make([]DailyInsight, days)}
	for i := range out.Daily {
		out.Daily[i] = DailyInsight{Day: from.AddDate(0, 0, i).Format(time.DateOnly), Requests: map[Provider]int64{ProviderAnthropic: 0, ProviderOpenAI: 0, ProviderGoogle: 0, ProviderOther: 0}}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT COALESCE(NULLIF(model,''),requested_model), session_id, COALESCE(turn_id,''), COALESCE(outcome,''), started_at, COALESCE(completed_at,'')
		FROM turn_records
		WHERE COALESCE(NULLIF(model,''),requested_model,'') != '' AND COALESCE(outcome,'') != 'imported'
		AND julianday(started_at) >= julianday(?) AND julianday(started_at) <= julianday(?)`, out.From, out.To)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	models := map[string]*modelAggregate{}
	sessions, turns := map[string]struct{}{}, map[string]struct{}{}
	for rows.Next() {
		var model, session, turn, outcome, started, completed string
		if err := rows.Scan(&model, &session, &turn, &outcome, &started, &completed); err != nil {
			return out, err
		}
		start, err := time.Parse(time.RFC3339Nano, started)
		if err != nil || start.Before(from) || start.After(now) {
			continue
		}
		m := models[model]
		if m == nil {
			m = &modelAggregate{ModelInsight: ModelInsight{Model: model, Provider: modelProvider(model)}, sessions: map[string]struct{}{}, turns: map[string]struct{}{}}
			models[model] = m
		}
		m.Requests++
		out.Totals.Requests++
		m.sessions[session] = struct{}{}
		sessions[session] = struct{}{}
		if turn != "" {
			m.turns[turn] = struct{}{}
			turns[turn] = struct{}{}
		}
		switch outcome {
		case "succeeded":
			end, err := time.Parse(time.RFC3339Nano, completed)
			if err == nil && !end.Before(start) {
				m.latency += end.Sub(start).Seconds()
				m.succeeded++
			}
		case "failed", "rejected":
			m.failed++
		case "canceled":
			m.canceled++
		}
		day := int(start.UTC().Sub(from) / (24 * time.Hour))
		out.Daily[day].Requests[m.Provider]++
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Totals.Sessions, out.Totals.Turns, out.Totals.Models = int64(len(sessions)), int64(len(turns)), int64(len(models))
	for _, m := range models {
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
	return out, nil
}
