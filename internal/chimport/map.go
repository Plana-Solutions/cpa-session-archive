package chimport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/deriver/extract"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
	"github.com/tidwall/gjson"
)

const timeLayout = "2006-01-02 15:04:05.000000000"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func mapRecord(a archive.Record, r *redact.Redactor) (requestRow, usageRow, error) {
	if a.StartedAt.IsZero() || a.CompletedAt.IsZero() {
		return requestRow{}, usageRow{}, errors.New("invalid archive timestamp")
	}
	h := map[string]string{}
	for k, v := range a.Facets {
		if strings.HasPrefix(k, "header.") && len(v) > 0 {
			h[strings.ToLower(strings.TrimPrefix(k, "header."))] = strings.Join(v, ", ")
		}
	}
	for _, k := range []string{"authorization", "proxy-authorization", "x-api-key", "api-key", "x-goog-api-key", "cookie", "set-cookie"} {
		delete(h, k)
	}
	h["x-plana-import"] = "archive-v1"
	if client := facet(a, "originator"); client != "" {
		h["originator"] = client
	}
	if a.Truncated {
		h["x-plana-archive-truncated"] = "true"
	}
	if ua := facet(a, "client.user_agent"); ua != "" {
		h["user-agent"] = ua
	}
	path, _ := a.Metadata["request_path"].(string)
	if path == "" {
		path = facet(a, "request.path")
	}
	row := requestRow{RequestID: a.RequestID, TraceID: a.TraceID, StartedAt: stamp(a.StartedAt), CompletedAt: stamp(a.CompletedAt), SourceFormat: a.SourceFormat, Model: a.Model, RequestedModel: a.RequestedModel, Path: path, Stream: a.Stream, Outcome: a.Outcome, StatusCode: a.StatusCode, Error: a.Error, RequestHeaders: h, RequestBody: string(a.OriginalRequest), ResponseHeaders: map[string]string{}, ResponseChunks: []string{}, RedactionRules: []string{}}
	if row.TraceID == "" {
		row.TraceID = a.RequestID
	}
	if a.Stream {
		row.ResponseChunks = splitStream(string(a.Response))
	} else {
		row.ResponseBody = string(a.Response)
	}
	u := usageRow{RequestID: "archive-v1:" + a.RequestID, TraceID: row.TraceID, RequestedAt: row.StartedAt, Model: a.Model, Alias: a.RequestedModel, ResponseModel: a.Model, SessionID: a.SessionID, Source: "archive-v1", Stream: a.Stream, Generate: !strings.Contains(path, "count_tokens") && !strings.Contains(strings.ToLower(path), "counttokens") && !strings.Contains(path, "embeddings"), LatencyMS: max(0, a.CompletedAt.Sub(a.StartedAt).Milliseconds()), Failed: a.Outcome == "failed" || a.Outcome == "rejected", ResponseHeaders: map[string]string{}, ReasoningEffort: facet(a, "reasoning.effort")}
	if u.Failed {
		u.FailureStatus = a.StatusCode
	}
	keyHash, _ := a.Metadata["key_hash"].(string)
	callerScope, _ := a.Metadata["caller_scope"].(string)
	if keyHash == "" && a.KeyID != callerScope && a.KeyID != facet(a, "caller.scope") {
		keyHash = a.KeyID
	}
	if key, err := hex.DecodeString(keyHash); err == nil && len(key) == 32 {
		u.APIKeyHash = hex.EncodeToString(key[:8])
	}
	u.AuthID = facet(a, "auth.id")
	u.Provider, _ = a.Metadata["target_provider"].(string)
	usageFrom(&u, a.Response)
	if raw, err := json.Marshal(a.Metadata); err == nil {
		usageFrom(&u, raw)
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	identity := extract.Conversation(extractRow(row))
	if (identity == "" || strings.HasPrefix(identity, "fp:")) && a.SessionID != "" && !strings.HasPrefix(a.SessionID, "request:") {
		row.RequestHeaders["x-session-id"] = a.SessionID
	}
	redactRequest(r, &row)
	if row.RequestHeaders == nil {
		row.RequestHeaders = map[string]string{}
	}
	row.RequestHeaders["x-plana-import"] = "archive-v1"
	redactUsage(r, &u)
	return row, u, nil
}
func extractRow(row requestRow) extract.Row {
	return extract.Row{RequestID: row.RequestID, Path: row.Path, SourceFormat: row.SourceFormat, RequestHeaders: row.RequestHeaders, RequestBody: row.RequestBody, ResponseBody: row.ResponseBody, ResponseChunks: row.ResponseChunks, RedactionCount: int32(row.RedactionCount)}
}

func facet(a archive.Record, k string) string {
	if v := a.Facets[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}
func splitStream(s string) []string {
	if s == "" {
		return []string{}
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.Contains(s, "data:") {
		return strings.Split(s, "\n\n")
	}
	return []string{s}
}
func usageFrom(u *usageRow, raw []byte) {
	apply := func(v gjson.Result) {
		for _, path := range []string{"usage", "response.usage", "message.usage", "usageMetadata"} {
			x := v.Get(path)
			if !x.Exists() {
				continue
			}
			fields := []struct {
				dst   *int64
				paths []string
			}{
				{&u.InputTokens, []string{"input_tokens", "prompt_tokens", "promptTokenCount"}},
				{&u.OutputTokens, []string{"output_tokens", "completion_tokens", "candidatesTokenCount"}},
				{&u.TotalTokens, []string{"total_tokens", "totalTokenCount"}},
				{&u.ReasoningTokens, []string{"output_tokens_details.reasoning_tokens", "completion_tokens_details.reasoning_tokens", "thoughtsTokenCount"}},
				{&u.CachedTokens, []string{"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cache_read_input_tokens", "cachedContentTokenCount"}},
				{&u.CacheReadTokens, []string{"cache_read_input_tokens"}},
				{&u.CacheCreationTokens, []string{"cache_creation_input_tokens"}},
			}
			for _, f := range fields {
				for _, p := range f.paths {
					*f.dst = max(*f.dst, x.Get(p).Int())
				}
			}
		}
	}
	apply(gjson.ParseBytes(raw))
	if !gjson.ValidBytes(raw) {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
			if gjson.Valid(line) {
				apply(gjson.Parse(line))
			}
		}
	}
}

type externalRow struct {
	SessionID       string `json:"session_id"`
	NativeSessionID string `json:"native_session_id"`
	Source          string `json:"source"`
	Project         string `json:"project"`
	Title           string `json:"title"`
	TS              string `json:"ts"`
	Position        uint32 `json:"position"`
	Role            string `json:"role"`
	Type            string `json:"type"`
	ToolName        string `json:"tool_name"`
	Text            string `json:"text"`
}

func mapExternal(a archive.Record, position uint32, r *redact.Redactor) (externalRow, error) {
	var payload struct {
		Event *archive.ExternalEvent `json:"external_event"`
	}
	if err := json.Unmarshal(a.OriginalRequest, &payload); err != nil || payload.Event == nil {
		return externalRow{}, errors.New("invalid native event")
	}
	e := payload.Event
	var session archive.ExternalSession
	raw, _ := json.Marshal(a.Metadata["external_session"])
	_ = json.Unmarshal(raw, &session)
	text := e.Text
	if text == "" {
		text = string(e.Data)
	}
	ts := e.Timestamp
	if ts.IsZero() {
		ts = a.StartedAt
	}
	if ts.IsZero() {
		return externalRow{}, errors.New("invalid native timestamp")
	}
	row := externalRow{SessionID: a.SessionID, NativeSessionID: session.ID, Source: facet(a, "source.tool"), Project: session.ProjectPath, Title: session.Title, TS: stamp(ts), Position: position, Role: e.Role, Type: gjson.GetBytes(e.Data, "type").String(), ToolName: e.Name, Text: text}
	redactExternal(r, &row)
	return row, nil
}
func redactExternal(r *redact.Redactor, row *externalRow) {
	defer func() {
		if recover() != nil {
			clearExternal(row)
		}
	}()
	if r == nil {
		clearExternal(row)
		return
	}
	texts := []*string{&row.Text, &row.Title, &row.Project, &row.ToolName, &row.Role, &row.Type, &row.Source, &row.NativeSessionID}
	values := []string{}
	for _, p := range texts {
		values = append(values, *p)
	}
	rep := r.NewReplacer(values)
	res := redact.NewResult()
	for _, p := range texts {
		*p = rep.Apply(*p, res)
	}
}

func clearExternal(row *externalRow) {
	*row = externalRow{SessionID: row.SessionID, Position: row.Position, TS: row.TS}
}
