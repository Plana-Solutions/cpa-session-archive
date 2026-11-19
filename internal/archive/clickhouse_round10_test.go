package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRound10Search(t *testing.T) {
	c := turnSearchFixture(t)
	at := time.Now().UTC().Add(-time.Hour)
	seed := func(id, text string, extra map[string]any) {
		row := map[string]any{"request_id": id, "conversation_id": id, "started_at": at.Format("2006-01-02 15:04:05.999999999"), "ingested_at": at.Format("2006-01-02 15:04:05.999999999"), "kind": "messages", "input_items": 200, "msg_direction": []string{"input"}, "msg_role": []string{"user"}, "msg_type": []string{"message"}, "msg_text": []string{text}}
		for k, v := range extra {
			row[k] = v
		}
		chInsert(t, c, "request_facts", row)
		at = at.Add(time.Second)
	}
	seed("mid-backfill", "captured history needle", map[string]any{"prior_seen": true, "msg_direction": []string{"input", "input"}, "msg_role": []string{"user", "tool"}, "msg_type": []string{"message", "tool_result"}, "msg_text": []string{"captured history needle", "result"}})
	statements := turnSearchSchema(t, c)
	seed("mid-mv", "captured history needle", map[string]any{"prior_seen": true, "msg_direction": []string{"input", "input"}, "msg_role": []string{"user", "tool"}, "msg_type": []string{"message", "tool_result"}, "msg_text": []string{"captured history needle", "result"}})
	seed("command", "<command-name>/review</command-name><command-args>this diff</command-args>", nil)
	seed("visible", "visibility needle", nil)
	for i := 0; i < 100; i++ {
		seed(fmt.Sprintf("hidden-%03d", i), "visibility needle", map[string]any{"thread_source": "system"})
	}
	for _, tag := range []string{"environment_context", "environment_info", "workspace_info", "in-app-browser-context", "app-context", "context", "editorcontext", "reminderinstructions", "codex_internal_context", "system-reminder", "local-command-caveat", "local-command-stdout", "skill"} {
		seed(tag, "<"+tag+">"+strings.Repeat("context ", 800)+"</"+tag+">Actual prompt "+strings.Repeat("genuine prompt ", 500), nil)
	}

	var inserted []struct {
		N uint64 `json:"n"`
	}
	if err := c.query(context.Background(), "SELECT count() AS n FROM cliproxy.turn_search WHERE request_id='mid-mv'", nil, &inserted); err != nil || len(inserted) != 1 || inserted[0].N != 1 {
		t.Fatalf("MV did not retain first captured history before backfill: %v %v", inserted, err)
	}
	chExec(t, c, "ALTER TABLE cliproxy.turn_search MODIFY COMMENT 'archive-turn-search-v1-building'")
	chExec(t, c, "DROP TABLE cliproxy.turn_search_mv")
	chExec(t, c, "DROP VIEW cliproxy.turn_search_source")
	statements = turnSearchSchema(t, c)
	if err := c.query(context.Background(), "SELECT count() AS n FROM cliproxy.turn_search WHERE request_id='mid-mv'", nil, &inserted); err != nil || inserted[0].N != 1 {
		t.Fatalf("upgrade changed existing search rows: %v %v", inserted, err)
	}
	backfill := statements[3]
	for k, v := range map[string]string{"from": at.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.999999999"), "to": at.Add(time.Hour).Format("2006-01-02 15:04:05.999999999"), "watermark": "2100-01-01 00:00:00"} {
		backfill = strings.ReplaceAll(backfill, "{"+k+":DateTime64(9,'UTC')}", "toDateTime64('"+v+"',9,'UTC')")
	}
	chExec(t, c, backfill)
	turnSearchActivate(t, c, statements)
	for _, compact := range []bool{false, true} {
		for _, tc := range []struct {
			name, term string
			want       int
		}{{"first captured request", "captured history needle", 2}, {"command without slash", "review this diff", 1}, {"hidden before limit", "visibility needle", 1}} {
			t.Run(fmt.Sprintf("%s/compact=%t", tc.name, compact), func(t *testing.T) {
				c.turnSearchChecked = time.Now()
				c.turnSearchPresent = compact
				got, err := c.read("sessions", url.Values{"q": {tc.term}, "limit": {"1"}})
				if err != nil {
					t.Fatal(err)
				}
				rows := got.([]archiveSession)
				if len(rows) != 1 || (tc.name == "hidden before limit" && rows[0].SessionID != "visible") {
					t.Fatalf("missing visible match: %+v", rows)
				}
				if tc.want == 2 {
					got, err = c.read("sessions", url.Values{"q": {tc.term}, "limit": {"20"}})
					if err != nil || len(got.([]archiveSession)) != 2 {
						t.Fatalf("first captured requests not searchable: %v %v", got, err)
					}
				}
			})
		}
	}
	t.Run("display wrappers", func(t *testing.T) {
		got, err := c.read("sessions", url.Values{"limit": {"200"}})
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, r := range got.([]archiveSession) {
			if strings.HasPrefix(r.Summary, "Actual prompt") {
				found++
			}
		}
		if found != 13 {
			t.Fatalf("visible wrapper titles=%d want 13", found)
		}
		got, err = c.read("turns", url.Values{"session_id": {"environment_context"}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(got)
		if !strings.Contains(string(b), "Actual prompt") {
			t.Fatal("turn prompt disappeared")
		}
	})
	var mv []struct {
		N uint64 `json:"n"`
	}
	if err := c.query(context.Background(), "SELECT count() AS n FROM cliproxy.turn_search WHERE request_id='mid-mv'", nil, &mv); err != nil || mv[0].N == 0 {
		t.Errorf("MV dropped first captured user/tool history: %v %v", mv, err)
	}
}

func TestRound10NativeSFT(t *testing.T) {
	c := turnSearchFixture(t)
	at := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
	for i, role := range []string{"metadata", "user", "compaction", "reasoning", "thinking", "tool_call", "tool_result", "assistant"} {
		chInsert(t, c, "external_messages", map[string]any{"session_id": "native-r10", "ts": at, "position": i, "role": role, "type": role, "tool_name": "shell", "text": "event-" + role})
	}
	content, _ := collectExport(t, c, url.Values{"id": {"external:native-r10"}, "format": {"sft"}})
	var sample SFTExample
	if err := json.Unmarshal([]byte(content), &sample); err != nil {
		t.Fatal(err)
	}
	for _, m := range sample.Messages {
		role := m["role"]
		if role != "user" && role != "assistant" {
			t.Errorf("unsupported or unlinked event leaked: %v", m)
		}
	}
	if len(sample.Messages) != 2 {
		t.Errorf("conversational messages=%d want 2", len(sample.Messages))
	}
	chInsert(t, c, "external_messages", map[string]any{"session_id": "no-assistant", "ts": at, "position": 0, "role": "user", "type": "message", "text": "question"})
	content, _ = collectExport(t, c, url.Values{"id": {"external:no-assistant"}, "format": {"sft"}})
	if content != "" {
		t.Errorf("invalid user-only training sample emitted: %s", content)
	}
	if _, err := c.read("export", url.Values{"id": {"external:no-assistant"}, "format": {"sft"}, "byte_offset": {"1"}}); err == nil {
		t.Fatal("invalid native sample silently ended a partial record")
	}
}

func TestRound10NativeToolSFT(t *testing.T) {
	c := turnSearchFixture(t)
	chExec(t, c, "ALTER TABLE cliproxy.external_messages ADD COLUMN call_id String")
	at := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
	events := []struct{ role, id, name, text string }{
		{"user", "", "", "question"},
		{"tool_call", "pair", "shell", `{"command":"pwd"}`},
		{"tool_result", "pair", "", "/project"},
		{"tool_result", "orphan", "", "must omit orphan"},
		{"tool_result", "reversed", "", "must omit reversed"},
		{"tool_call", "reversed", "shell", "must omit reversed"},
		{"tool_call", "unfinished", "shell", "must omit unfinished"},
		{"tool_call", "parallel-a", "shell", "must omit parallel"},
		{"tool_call", "parallel-b", "shell", "must omit parallel"},
		{"tool_result", "parallel-a", "", "must omit parallel"},
		{"tool_result", "parallel-b", "", "must omit parallel"},
		{"assistant", "", "", "answer"},
	}
	for i, e := range events {
		chInsert(t, c, "external_messages", map[string]any{"session_id": "paired", "ts": at, "position": i, "role": e.role, "type": e.role, "call_id": e.id, "tool_name": e.name, "text": e.text})
	}
	content, _ := collectExport(t, c, url.Values{"id": {"external:paired"}, "format": {"sft"}})
	var sample SFTExample
	if err := json.Unmarshal([]byte(content), &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.Messages) != 4 {
		t.Fatalf("expected conversation and paired tools: %s", content)
	}
	call := sample.Messages[1]
	result := sample.Messages[2]
	calls, ok := call["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("missing conversational tool call: %v", call)
	}
	tool := calls[0].(map[string]any)
	if call["role"] != "assistant" || tool["id"] != "pair" || tool["function"].(map[string]any)["name"] != "shell" || result["role"] != "tool" || result["tool_call_id"] != "pair" || result["content"] != "/project" {
		t.Fatalf("tool pair changed: %s", content)
	}
	if strings.Contains(content, "must omit") {
		t.Fatalf("invalid tool pairs exported: %s", content)
	}
}
