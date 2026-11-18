package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickHouseRound5(t *testing.T) {
	c := localClickHouse(t)
	for _, file := range []string{"schema.sql", "derived.sql", "external.sql"} {
		b, _ := os.ReadFile("testdata/clickhouse/" + file)
		for _, sql := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
		}
	}
	base := time.Now().UTC().Add(-time.Hour)
	seed := func(id, session string, n int, status int, input, output string, extra map[string]any) {
		at := base.Add(time.Duration(n) * time.Second).Format("2006-01-02 15:04:05.999999999")
		chInsert(t, c, "requests", map[string]any{"request_id": id, "trace_id": "shared-" + session, "started_at": at, "completed_at": at, "request_body": input, "response_body": output})
		row := map[string]any{"request_id": id, "conversation_id": session, "started_at": at, "ingested_at": at, "kind": "responses", "outcome": "succeeded", "status_code": status, "input_items": 1, "msg_direction": []string{"input", "output"}, "msg_position": []int{0, 0}, "msg_role": []string{"user", "assistant"}, "msg_type": []string{"message", "message"}, "msg_text": []string{"prompt-" + id, "answer-" + id}, "msg_tool_name": []string{"", ""}, "msg_call_id": []string{"", ""}, "msg_arguments": []string{"", ""}, "msg_is_error": []bool{false, false}, "msg_text_hash": []int{0, 0}}
		for k, v := range extra {
			row[k] = v
		}
		chInsert(t, c, "request_facts", row)
	}
	t.Run("latest successful snapshot", func(t *testing.T) {
		for i, status := range []int{200, 201, 500} {
			id := fmt.Sprint("sft-", i)
			seed(id, "sft-session", i, status, `{"input":[{"role":"user","content":"history"},{"role":"assistant","content":"prior answer"},{"role":"user","content":"latest question"}]}`, `{"output":[{"role":"assistant","content":"latest answer"}]}`, nil)
		}
		content, _ := collectExport(t, c, url.Values{"id": {"sft-session"}, "format": {"sft"}})
		if strings.Count(content, "prior answer") != 1 || strings.Contains(content, "answer-sft-") || !strings.Contains(content, "latest answer") {
			t.Fatalf("SFT did not select latest successful snapshot: %s", content)
		}
	})
	t.Run("reasoning excluded", func(t *testing.T) {
		seed("reasoning", "reason-session", 10, 200, `{}`, `data: stream`, map[string]any{"msg_direction": []string{"input", "output", "output", "output"}, "msg_position": []int{0, 0, 1, 2}, "msg_role": []string{"user", "assistant", "assistant", "assistant"}, "msg_type": []string{"message", "reasoning", "thinking", "message"}, "msg_text": []string{"question", "SECRET-REASONING", "SECRET-THINKING", "answer"}, "msg_tool_name": []string{"", "", "", ""}, "msg_call_id": []string{"", "", "", ""}, "msg_arguments": []string{"", "", "", ""}, "msg_is_error": []bool{false, false, false, false}, "msg_text_hash": []int{0, 0, 0, 0}})
		content, _ := collectExport(t, c, url.Values{"id": {"reason-session"}, "format": {"sft"}})
		if strings.Contains(content, "SECRET-") || !strings.Contains(content, "answer") {
			t.Fatalf("reasoning in SFT: %s", content)
		}
	})
	t.Run("usage attribution shared trace", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			id := fmt.Sprint("usage-", i)
			seed(id, "usage-session", 20+i, 200, `{}`, `{}`, nil)
			chInsert(t, c, "usage", map[string]any{"request_id": "attempt-" + id, "trace_id": "shared-usage-session", "requested_at": base.Add(time.Duration(20+i) * time.Second).Format("2006-01-02 15:04:05.999999999"), "total_tokens": 10 + i})
		}
		w, _ := windowFor(nil, 365)
		w.params.Set("id", "usage-0")
		w, _ = c.narrow(context.Background(), w, `request_id={id:String}`)
		var rows []struct {
			Tokens int `json:"tokens"`
		}
		if err := c.query(context.Background(), `SELECT u.total_tokens AS tokens `+factsWithUsage+` WHERE `+timeFilter+` AND f.request_id={id:String}`, w.params, &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Tokens != 10 {
			t.Fatalf("later request inflated tokens: %+v", rows)
		}
	})
	t.Run("stateful and explicit turns", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			seed(fmt.Sprint("state-", i), "state-session", 30+i, 200, `{}`, `{}`, map[string]any{"prior_seen": i > 0, "previous_response_id": "previous", "turn_id": fmt.Sprint("turn-", i), "reply_text": fmt.Sprint("reply-", i)})
		}
		got, err := c.read("turns", url.Values{"session_id": {"state-session"}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(got)
		var page struct{ Total int }
		json.Unmarshal(b, &page)
		if page.Total != 3 {
			t.Fatalf("stateful followups merged: %s", b)
		}
	})
	t.Run("shortened request entries", func(t *testing.T) {
		seed("truncated", "long-session", 40, 200, `{}`, `{}`, map[string]any{"msg_text": []string{strings.Repeat("界", 5000), ""}, "msg_role": []string{"user", "assistant"}, "msg_type": []string{"message", "function_call"}, "msg_tool_name": []string{"", "shell"}, "msg_arguments": []string{"", strings.Repeat("a", 5000)}})
		got, err := c.read("request-view", url.Values{"id": {"truncated"}})
		if err != nil {
			t.Fatal(err)
		}
		entries := got.(RequestTimelineView).Entries
		if len(entries) != 2 || !entries[0].Truncated || !entries[1].Truncated {
			t.Fatalf("silent truncation: %+v", entries)
		}
	})
	t.Run("native event projection", func(t *testing.T) {
		at := base.Format("2006-01-02 15:04:05.999999999")
		chInsert(t, c, "external_messages", map[string]any{"session_id": "native-r5", "native_session_id": "source-id", "source": "codex", "project": "project", "title": "title", "ts": at, "position": 1, "role": "tool", "type": "tool_result", "tool_name": "shell", "text": "result"})
		content, _ := collectExport(t, c, url.Values{"id": {"external:native-r5"}, "format": {"archive"}})
		var record struct {
			Original []byte    `json:"original_request"`
			Started  time.Time `json:"started_at"`
		}
		if err := json.Unmarshal([]byte(content), &record); err != nil {
			t.Fatal(err)
		}
		var event externalMessage
		json.Unmarshal(record.Original, &event)
		if event.Type != "tool_result" || event.ToolName != "shell" || event.Source != "codex" || event.NativeSessionID != "source-id" || !event.TS.Equal(base) || !record.Started.Equal(base) {
			t.Fatalf("lost native metadata: event=%+v started=%v body=%s", event, record.Started, base64.StdEncoding.EncodeToString(record.Original))
		}
	})
	t.Run("search uses cleaned titles in both paths", func(t *testing.T) {
		seed("search", "search-session", 50, 200, `{}`, `{}`, map[string]any{"last_user_text": "<system-reminder>injected preamble</system-reminder>", "msg_direction": []string{"input", "input"}, "msg_role": []string{"user", "user"}, "msg_type": []string{"message", "message"}, "msg_text": []string{"<system-reminder>context</system-reminder>Find genuine needle", "<system-reminder>injected preamble</system-reminder>"}})
		for _, limit := range []string{"20", "641"} {
			got, err := c.read("sessions", url.Values{"q": {"Find genuine needle"}, "limit": {limit}})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.([]archiveSession)) != 1 {
				t.Fatalf("displayed title missing from search limit=%s", limit)
			}
		}
	})
}
