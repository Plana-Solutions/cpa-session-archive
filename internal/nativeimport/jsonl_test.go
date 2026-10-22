package nativeimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestCodexPreservesEveryNativeKindAndOriginalData(t *testing.T) {
	const input = `{"timestamp":"2026-10-22T10:00:00Z","type":"session_meta","payload":{"id":"own-thread","parent_thread_id":"parent-thread","cwd":"/fixture/project"}}
{"timestamp":"2026-10-22T10:00:01Z","type":"response_item","payload":{"id":"message-1","type":"message","role":"user","content":[{"type":"input_text","text":"Analyze archive fixture"}]}}
{"timestamp":"2026-10-22T10:00:02Z","type":"response_item","payload":{"id":"call-1","type":"function_call","name":"shell","call_id":"pair-1","arguments":"{\"cmd\":\"pwd\"}"}}
{"timestamp":"2026-10-22T10:00:03Z","type":"response_item","payload":{"id":"output-1","type":"function_call_output","call_id":"pair-1","output":"/fixture/project"}}
{"timestamp":"2026-10-22T10:00:04Z","type":"response_item","payload":{"id":"reason-1","type":"reasoning","summary":[{"type":"summary_text","text":"Fixture reasoning"}],"encrypted_content":"exact-opaque-fixture"}}
{"timestamp":"2026-10-22T10:00:05Z","type":"world_state","payload":{"unknown_future_field":true}}
`
	sessions, err := parseJSONL(context.Background(), strings.NewReader(input), archive.ExternalSource{Tool: "codex", Path: "/fixture/rollout.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Events) != 6 {
		t.Fatalf("sessions/events=%d/%d", len(sessions), len(sessions[0].Events))
	}
	s := sessions[0]
	if s.ID != "own-thread" || s.ParentID != "parent-thread" || s.ProjectPath != "/fixture/project" {
		t.Fatalf("incorrect source provenance")
	}
	if s.Events[1].Text != "Analyze archive fixture" || s.Events[2].Role != "tool_call" || s.Events[3].CallID != "pair-1" {
		t.Fatal("native message/tool projection failed")
	}
	if !strings.Contains(string(s.Events[4].Data), `"encrypted_content":"exact-opaque-fixture"`) || s.Events[5].Role != "world_state" {
		t.Fatal("opaque or unknown native data lost")
	}
}

func TestCodexFilenameKeepsOwnSessionAcrossInheritedMetadata(t *testing.T) {
	const own = "01234567-89ab-cdef-0123-456789abcdef"
	input := `{"type":"session_meta","payload":{"id":"ancestor"}}` + "\n" + `{"type":"response_item","payload":{"id":"a","type":"message","role":"assistant","content":"Inherited context"}}` + "\n"
	sessions, err := parseJSONL(context.Background(), strings.NewReader(input), archive.ExternalSource{Tool: "codex", Path: "/fixture/rollout-" + own + ".jsonl"})
	if err != nil || len(sessions) != 1 || sessions[0].ID != own {
		t.Fatal("inherited session metadata changed file session identity", err)
	}
}

func TestClaudeUUIDRelationshipsChildAndMixedBlocks(t *testing.T) {
	input := `{"type":"assistant","uuid":"row-a","parentUuid":"row-parent","sessionId":"session-a","agentId":"child-a","timestamp":"2026-10-22T10:00:00Z","message":{"id":"api-shared","role":"assistant","content":[{"type":"thinking","thinking":"Fixture reasoning","signature":"opaque-fixture"},{"type":"tool_use","id":"tool-a","name":"Read","input":{"file_path":"/fixture/a"}}]}}
{"type":"assistant","uuid":"row-b","parentUuid":"row-a","sessionId":"session-a","agentId":"child-a","message":{"id":"api-shared","role":"assistant","content":[{"type":"text","text":"Fixture answer"}]}}
{"type":"queue-operation","sessionId":"session-a","agentId":"child-a","operation":"enqueue"}
`
	sessions, err := parseJSONL(context.Background(), strings.NewReader(input), archive.ExternalSource{Tool: "claude", Path: "/fixture/agent-child-a.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-a/agent/child-a" || sessions[0].ParentID != "session-a" || len(sessions[0].Events) != 3 {
		t.Fatal("child source identity or native rows lost")
	}
	events := sessions[0].Events
	if events[0].ID == events[1].ID || events[1].ParentID != events[0].ID || !strings.Contains(events[0].Text, "Fixture reasoning") || !strings.Contains(events[0].Text, "Read") {
		t.Fatal("row UUID relationships or block content lost")
	}
	if !strings.HasPrefix(events[2].ID, "byte-") {
		t.Fatal("missing source IDs must use stable byte offsets")
	}
}

func TestPrepareRejectsMalformedJSONLWithoutRawFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("{malformed}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(context.Background(), archive.ExternalSource{Tool: "codex", Path: path}, false)
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatal("invalid native data silently accepted")
	}
	artifact, err := Prepare(context.Background(), archive.ExternalSource{Tool: "other", Path: path}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	if len(artifact.Sessions) != 0 || len(artifact.Warnings) == 0 {
		t.Fatal("raw-only mode must explicitly disclose missing searchable projection")
	}
}

func TestJSONLAppendKeepsExistingEventIdentity(t *testing.T) {
	first := `{"type":"queue-operation","sessionId":"session-a","operation":"enqueue"}` + "\n"
	second := `{"type":"queue-operation","sessionId":"session-a","operation":"dequeue"}` + "\n"
	source := archive.ExternalSource{Tool: "claude", Path: "/fixture/a.jsonl"}
	a, err := parseJSONL(context.Background(), strings.NewReader(first), source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseJSONL(context.Background(), strings.NewReader(first+second), source)
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Events[0].ID != b[0].Events[0].ID || a[0].Events[0].ID == b[0].Events[1].ID {
		t.Fatal("append changed existing native event identity")
	}
	if !json.Valid(b[0].Events[0].Data) {
		t.Fatal("original native row not retained as JSON")
	}
}

func TestClaudeNativeToolPair(t *testing.T) {
	input := `{"uuid":"call-row","sessionId":"s","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"path":"fixture"}}]}}` + "\n" +
		`{"uuid":"result-row","sessionId":"s","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-a","content":"fixture result"}]}}` + "\n"
	sessions, err := parseJSONL(context.Background(), strings.NewReader(input), archive.ExternalSource{Tool: "claude", Path: "/fixture/claude.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	call, result := sessions[0].Events[0], sessions[0].Events[1]
	if call.Role != "tool_call" || call.Name != "Read" || result.Role != "tool_result" || call.CallID != result.CallID || result.Text != "fixture result" {
		t.Fatal("Claude native tool pair missing")
	}
}
