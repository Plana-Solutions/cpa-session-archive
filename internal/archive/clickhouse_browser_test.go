package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The browser harness serves the actual handlers over a disposable ClickHouse database.
func TestClickHouseBrowserServer(t *testing.T) {
	if os.Getenv("ARCHIVE_BROWSER_SERVER") == "" {
		t.Skip("browser server opt-in required")
	}
	c := localClickHouse(t)
	c.client.Transport = browserFixtureTransport{}
	for _, file := range []string{"schema.sql", "derived.sql", "external.sql"} {
		b, err := os.ReadFile("testdata/clickhouse/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(stmt, "{db}", "cliproxy"))
		}
	}
	b, err := exec.Command("node", "../../e2e/fixture-source.cjs").Output()
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		LongUser, LongAssistant, LongSystem, LongToolOutput, EscapedSummary, EscapedEnvironment string
		OriginalRequest, OriginalResponse                                                       json.RawMessage
	}
	if err = json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	models := []string{"gpt-6.1-sol", "claude-opus-5-5", "claude-sonnet-5-5", "gpt-6-astra", "gemini-3-pro", "kimi-k3"}
	for m, model := range models {
		n := 20
		if m == 0 {
			n = 41
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("step-%d-%d", m, i)
			if m == 0 && i == 0 {
				id = "mock-request-long-content"
			}
			at := base.Add(time.Duration(m*100+i) * time.Second).Format("2006-01-02 15:04:05.999999999")
			session, turn := "", ""
			if m == 0 {
				session = "mock-session-long-content"
				turn = "mock-turn-long-content"
			}
			chInsert(t, c, "requests", map[string]any{"request_id": id, "trace_id": id, "started_at": at, "completed_at": at, "model": model, "requested_model": "gpt-5.6-sol", "request_body": string(source.OriginalRequest), "response_body": string(source.OriginalResponse), "outcome": "succeeded", "status_code": 200})
			user := source.EscapedSummary + " mock-long-project " + source.EscapedEnvironment + " " + source.LongUser
			chInsert(t, c, "request_facts", map[string]any{"request_id": id, "started_at": at, "ingested_at": at, "conversation_id": session, "turn_id": turn, "prior_seen": i > 0, "model": model, "client": "codex", "kind": "responses", "request_kind": "turn", "outcome": "succeeded", "status_code": 200, "last_user_text": user, "reply_text": source.LongAssistant, "reasoning_text": source.LongAssistant, "duration_ms": 2500, "input_items": 200, "tools_offered": []string{"shell_command"}, "tools_called": []string{"shell_command"}, "msg_direction": []string{"input", "input", "output", "output", "output"}, "msg_position": []int{0, 1, 0, 1, 2}, "msg_role": []string{"system", "user", "assistant", "tool", "assistant"}, "msg_type": []string{"message", "message", "function_call", "function_call_output", "message"}, "msg_text": []string{source.LongSystem, user, "", source.LongToolOutput, source.LongAssistant}, "msg_tool_name": []string{"", "", "shell_command", "shell_command", ""}, "msg_call_id": []string{"", "", "call-long-output", "call-long-output", ""}, "msg_arguments": []string{"", "", `{"command":"printf fixture"}`, "", ""}, "msg_is_error": []bool{false, false, false, false, false}, "msg_text_hash": []int{0, 0, 0, 0, 0}})
			chInsert(t, c, "usage", map[string]any{"request_id": id, "trace_id": id, "requested_at": at, "api_key_hash": "mock-key-hash", "input_tokens": 10, "total_tokens": 12})
		}
	}
	for i, role := range []string{"user", "assistant"} {
		text := "Native prompt"
		if i == 1 {
			text = "Native answer"
		}
		chInsert(t, c, "external_messages", map[string]any{"session_id": "imported-session", "source": "claude-import", "title": "Imported native transcript", "project": "mock-long-project", "position": i + 1, "role": role, "type": "message", "text": text, "ts": base.Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")})
	}
	p := &Plugin{reader: c}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/healthz" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		if path == "/v0/management/api-key-aliases" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"items":[{"apiKeyHash":"sha256:mock-key-hash","alias":"长文本测试 Key"}]}`))
			return
		}
		if path == "/" || path == "/management.html" {
			path = "/v0/resource/plugins/" + pluginID + "/index.html"
		}
		if path == "/external.js" {
			path = "/v0/resource/plugins/" + pluginID + "/external.js"
		}
		raw, _ := json.Marshal(managementRequest{Method: r.Method, Path: path, Headers: r.Header, Query: r.URL.Query()})
		data, e := p.HandleContext(r.Context(), "management.handle", raw)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		var env struct{ Result managementResponse }
		json.Unmarshal(data, &env)
		for key, v := range env.Result.Headers {
			w.Header()[key] = v
		}
		w.WriteHeader(env.Result.StatusCode)
		w.Write(env.Result.Body)
	})
	server := &http.Server{Addr: "127.0.0.1:4173", Handler: mux}
	t.Cleanup(func() { server.Close() })
	t.Fatal(server.ListenAndServe())
}

// This transport only observes the synthetic browser fixture, which has no reader credentials.
type browserFixtureTransport struct{}

func (s browserFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && resp.StatusCode != http.StatusOK {
		body, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil {
			return nil, e
		}
		fmt.Fprintf(os.Stderr, "ClickHouse fixture failure HTTP %d: %s\n", resp.StatusCode, body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, err
}
