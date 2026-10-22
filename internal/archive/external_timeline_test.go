package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExternalTimelinePreservesNativeEvents(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	events := []ExternalEvent{
		{ID: "native-user", Timestamp: now, Role: "user", Text: "repeat this exact text"},
		{ID: "native-repeat", ParentID: "native-user", Timestamp: now.Add(time.Second), Role: "user", Text: "repeat this exact text"},
		{ID: "native-call", Timestamp: now.Add(2 * time.Second), Role: "tool_call", Text: `{"cmd":"pwd"}`, Name: "shell", CallID: "original-call"},
		{ID: "native-result", Timestamp: now.Add(3 * time.Second), Role: "tool_result", Text: "/project", Name: "shell", CallID: "original-call"},
		{ID: "native-future", Timestamp: now.Add(4 * time.Second), Role: "future_role", Data: json.RawMessage(`{"type":"future-event","value":42}`)},
		{ID: "native-long", Timestamp: now.Add(5 * time.Second), Role: "assistant", Text: strings.Repeat("α", 45000)},
	}
	var records []Record
	for i, event := range events {
		body, marshalErr := json.Marshal(map[string]any{"external_event": event})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		records = append(records, Record{
			RequestID: fmt.Sprintf("external-%d", i), SessionID: "external-session",
			StartedAt: event.Timestamp, CompletedAt: event.Timestamp,
			OriginalRequest: body, SourceFormat: "external-native-event", Outcome: "imported",
			Facets: map[string][]string{"source.kind": {"external-local-history"}},
		})
	}
	if err := store.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	for i, event := range events {
		id := records[i].RequestID
		view, err := store.RequestTimeline(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := store.RequestTimelinePreview(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view, preview) {
			t.Fatalf("event %s preview differs from native timeline", event.ID)
		}
		if view.Kind != "native_event" || view.StatusCode != 0 || view.Outcome != "imported" || view.HistoryItems != 0 || len(view.Entries) != 1 {
			t.Fatalf("event %s received transport semantics: %#v", event.ID, view)
		}
		entry := view.Entries[0]
		wantText := event.Text
		if wantText == "" {
			wantText = string(event.Data)
		}
		if entry.EventID != event.ID || entry.ParentID != event.ParentID || entry.Timestamp == nil || !entry.Timestamp.Equal(event.Timestamp) || entry.Role != event.Role || entry.Text != wantText || entry.Label != event.Name || entry.CallID != event.CallID || entry.Truncated {
			t.Fatalf("event %s changed in timeline", event.ID)
		}
	}
}
