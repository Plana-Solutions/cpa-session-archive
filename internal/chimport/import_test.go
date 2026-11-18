package chimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
)

func fixture(t *testing.T) archive.Record {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, "2026-11-18T22:27:01.123456789+08:00")
	if err != nil {
		t.Fatal(err)
	}
	return archive.Record{RequestID: "req1", TraceID: "trace", SessionID: "session", KeyID: strings.Repeat("a", 64), StartedAt: ts, CompletedAt: ts.Add(time.Second), Model: "m", SourceFormat: "openai-response", Stream: true, Outcome: "succeeded", StatusCode: 200, OriginalRequest: []byte(`{"input":"hello"}`), Response: []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"total_tokens\":13}}}\n\n"), Metadata: map[string]any{"request_path": "/v1/responses"}, Facets: map[string][]string{"header.authorization": {"Bearer secret"}}}
}
func TestMappingTimezoneUsageAndRedaction(t *testing.T) {
	r, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	secret := "known-private-value-0123456789012345"
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("api-keys:\n  - "+secret+"\n"), 0600)
	r.Configure([]string{path}, nil)
	a := fixture(t)
	a.OriginalRequest = []byte(`{"input":"` + secret + `"}`)
	row, u, err := mapRecord(a, r)
	if err != nil {
		t.Fatal(err)
	}
	if row.StartedAt != "2026-11-18 14:27:01.123456789" || row.CompletedAt != "2026-11-18 14:27:02.123456789" {
		t.Fatal(row.StartedAt, row.CompletedAt)
	}
	if strings.Contains(row.RequestBody, secret) || row.RedactionCount != 1 || row.RequestHeaders["authorization"] != "" || row.RequestHeaders["x-plana-import"] != "archive-v1" {
		t.Fatal("redaction or marker missing")
	}
	if u.TotalTokens != 13 || u.InputTokens != 10 || u.OutputTokens != 3 || u.APIKeyHash != strings.Repeat("a", 16) || u.TraceID != row.TraceID {
		t.Fatal(u)
	}
	if row.ResponseBody != "" || len(row.ResponseChunks) < 1 {
		t.Fatal("stream mapping")
	}
	a.Stream = false
	a.Response = []byte(`{"usage":{"prompt_tokens":8,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":1}}}`)
	row, u, err = mapRecord(a, r)
	if err != nil || row.ResponseBody == "" || u.TotalTokens != 10 || u.ReasoningTokens != 1 {
		t.Fatal("nonstream mapping", err)
	}
	a.StartedAt = time.Time{}
	if _, _, err = mapRecord(a, r); err == nil {
		t.Fatal("accepted invalid timestamp")
	}
}
func TestRedactionFailureClosesBodies(t *testing.T) {
	row, _, err := mapRecord(fixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.RedactionCount != -1 || row.RequestBody != "" || len(row.ResponseChunks) != 0 || len(row.RequestHeaders) != 1 || row.RequestHeaders["x-plana-import"] != "archive-v1" || row.Error != "" {
		t.Fatal("fail open")
	}
	a := fixture(t)
	a.OriginalRequest = []byte(`{"external_event":{"role":"tool","name":"tool","data":{"type":"tool_result","output":"hello"}}}`)
	a.Metadata["external_session"] = map[string]any{"id": "native", "title": "title", "project_path": "/project"}
	a.Facets["source.tool"] = []string{"codex"}
	r, _ := redact.New()
	x, err := mapExternal(a, 7, r)
	if err != nil || x.Position != 7 || x.Type != "tool_result" || !strings.Contains(x.Text, "hello") || x.SessionID != a.SessionID {
		t.Fatal(x, err)
	}
	x, err = mapExternal(a, 7, nil)
	if err != nil || x.Text != "" || x.Title != "" {
		t.Fatal("native fail open")
	}
}
func TestResumeOverlapAndPartialBatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "archive.sqlite")
	s, err := archive.OpenStore(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	b := a
	b.RequestID = "req2"
	b.TraceID = "trace2"
	if err = s.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: []"), 0600)
	requests := map[string]bool{"req2": false}
	used := map[string]bool{}
	failUsage := true
	var insertedRequests, insertedUsage int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "system.disks") {
			io.WriteString(w, `{"free":100000000000}`)
			return
		}
		if strings.Contains(q, "system.parts") {
			return
		}
		if strings.HasPrefix(q, "SELECT") {
			if strings.Contains(q, "cliproxy.requests") {
				for id, imported := range requests {
					json.NewEncoder(w).Encode(map[string]any{"request_id": id, "imported": map[bool]int{true: 1, false: 0}[imported]})
				}
			} else {
				for id := range used {
					json.NewEncoder(w).Encode(map[string]any{"request_id": id})
				}
			}
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" || r.URL.Query().Get("insert_deduplication_token") == "" {
			t.Error("insert lacks gzip/dedup")
		}
		z, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		raw, _ := io.ReadAll(z)
		z.Close()
		if strings.Contains(q, "cliproxy.usage") && failUsage {
			w.WriteHeader(500)
			return
		}
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
			var m map[string]any
			json.Unmarshal(line, &m)
			id, _ := m["request_id"].(string)
			if strings.Contains(q, "cliproxy.requests") {
				requests[id] = true
				insertedRequests++
			} else {
				used[id] = true
				insertedUsage++
			}
		}
	}))
	defer server.Close()
	o := Options{SQLite: dbPath, State: filepath.Join(dir, "state"), URL: server.URL, Config: cfg, AuthDir: dir, Batch: 2, ExternalSessions: -1}
	if err = Run(context.Background(), o); err == nil {
		t.Fatal("expected interrupted usage insert")
	}
	state, err := loadState(o, s.DB)
	if err != nil || state.LastID != 0 {
		t.Fatal("checkpoint advanced after failed insert", err)
	}
	failUsage = false
	if err = Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if err = Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if insertedRequests != 1 || insertedUsage != 1 || !used["archive-v1:req1"] || used["archive-v1:req2"] {
		t.Fatal(insertedRequests, insertedUsage, used)
	}
	ro, err := openReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.DB.Close()
	if _, err = ro.DB.Exec("DELETE FROM records"); err == nil {
		t.Fatal("writable input")
	}
	o.URL += "/other"
	if _, err = loadState(o, ro.DB); err == nil {
		t.Fatal("accepted wrong target checkpoint")
	}
}

func TestUsageFormats(t *testing.T) {
	for _, tc := range []struct {
		name, body           string
		input, output, cache int64
	}{
		{"claude", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":8}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n", 12, 5, 8},
		{"gemini", `{"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":4,"cachedContentTokenCount":9}}`, 20, 4, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixture(t)
			a.Response = []byte(tc.body)
			_, u, err := mapRecord(a, nil)
			if err != nil {
				t.Fatal(err)
			}
			if u.InputTokens != tc.input || u.OutputTokens != tc.output || u.CachedTokens != tc.cache || u.TotalTokens != tc.input+tc.output {
				t.Fatal("token counts do not reconcile", u)
			}
		})
	}
	a := fixture(t)
	a.Response = nil
	a.Metadata["usage"] = map[string]any{"input_tokens": 6, "output_tokens": 2}
	_, u, err := mapRecord(a, nil)
	if err != nil || u.TotalTokens != 8 {
		t.Fatal("metadata usage missing", err)
	}
}

func TestReadCASAndLegacyGzip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	if err = s.PutBatch([]archive.Record{a}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Request(context.Background(), a.RequestID)
	if err != nil || !bytes.Equal(got.OriginalRequest, a.OriginalRequest) || !bytes.Equal(got.Response, a.Response) {
		t.Fatal("CAS reconstruction", err)
	}
	compress := func(raw []byte) []byte {
		var out bytes.Buffer
		z := gzip.NewWriter(&out)
		z.Write(raw)
		z.Close()
		return out.Bytes()
	}
	_, err = s.DB.Exec("UPDATE records SET original_ref='',response_ref='',original_request_gz=?,response_gz=? WHERE request_id=?", compress(a.OriginalRequest), compress(a.Response), a.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	ro, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.DB.Close()
	got, err = ro.Request(context.Background(), a.RequestID)
	if err != nil || !bytes.Equal(got.OriginalRequest, a.OriginalRequest) || !bytes.Equal(got.Response, a.Response) {
		t.Fatal("legacy reconstruction", err)
	}
}
