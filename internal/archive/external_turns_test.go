package archive

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExternalTurnsKeepNativeOrderAndRoles(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	events := []ExternalEvent{
		{ID: "meta", Role: "metadata", Timestamp: now, Text: "session metadata"},
		{ID: "u1", Role: "user", Timestamp: now.Add(-time.Hour), Text: "first question"},
		{ID: "a1", Role: "assistant", Timestamp: now, Text: "answer"},
		{ID: "t1", Role: "tool_result", Timestamp: now, Text: "tool output", Name: "shell"},
		{ID: "u2", Role: "user", Timestamp: now, Text: "second question"},
		{ID: "t2", Role: "compaction", Timestamp: now, Text: "context retained"},
	}
	receipt, err := s.ImportExternal(ctx, ExternalSource{Host: "test", Account: "review", Tool: "codex", Path: "/fixture.jsonl", Format: "codex-jsonl"}, strings.NewReader("raw fixture"), []ExternalSession{{ID: "native", Events: events}})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.sessionTurnGroups(ctx, receipt.SessionIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 || groups[1].summary.UserText != "first question" || groups[1].summary.FinalText != "answer" || groups[1].summary.Requests != 3 || !groups[2].summary.HasCompaction {
		t.Fatalf("incorrect native groups: %+v", groups)
	}
	if groups[1].summary.StatusCode != 0 || groups[1].summary.Outcome != "imported" || groups[1].summary.FirstAt != events[1].Timestamp {
		t.Fatal("native timing or outcome was changed")
	}
	for _, group := range groups {
		detail, err := s.SessionTurnDetail(ctx, receipt.SessionIDs[0], group.summary.TurnID, 100, 0)
		if err != nil || len(detail.Records) != len(group.requestIDs) {
			t.Fatalf("native detail: %v", err)
		}
	}
}
