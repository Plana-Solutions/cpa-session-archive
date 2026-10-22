package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func externalTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	s, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s, path
}

func externalTestSource() ExternalSource {
	return ExternalSource{Host: "m1", Account: "minoo", Tool: "codex", Path: "/history/session.jsonl", Format: "codex-jsonl", ModifiedAt: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)}
}

func externalTestSession() ExternalSession {
	return ExternalSession{ID: "native-id", ParentID: "parent-native-id", Title: "Archive import design", ProjectPath: "/dev/archive", Events: []ExternalEvent{
		{ID: "user-1", Timestamp: externalTestSource().ModifiedAt, Role: "user", Text: "Keep original history"},
		{ID: "unknown-1", ParentID: "user-1", Timestamp: externalTestSource().ModifiedAt.Add(time.Second), Role: "future-unknown-event", Name: "native_tool", CallID: "call-1", Text: "Result", Data: json.RawMessage(`{"encrypted_content":"opaque-native-value","type":"unknown_native_type","large_number":9007199254740993}`)},
	}}
}

func TestExternalRawRoundTripIsLosslessAndChunked(t *testing.T) {
	for _, fixture := range []struct{ name, raw string }{
		{"json", " {\n \"encrypted_content\": \"retain exactly\", \"text\": \"x\" }\r\n"},
		{"sse", "event: response.created\ndata: {\"encrypted_content\":\"first\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"encrypted_content\":\"last\"}\n\n"},
		{"multi-chunk", strings.Repeat("opaque\x00encrypted_content\r\n", externalChunkSize/10)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s, _ := externalTestStore(t)
			session := externalTestSession()
			receipt, err := s.ImportExternal(context.Background(), externalTestSource(), strings.NewReader(fixture.raw), []ExternalSession{session})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Replayed || receipt.ImportedEvents != 2 || receipt.Source.Bytes != int64(len(fixture.raw)) {
				t.Fatalf("receipt=%+v", receipt)
			}
			var out bytes.Buffer
			if err = s.WriteExternalSource(context.Background(), receipt.Source.ID, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != fixture.raw {
				t.Fatal("raw source bytes changed")
			}
			sum := sha256.Sum256([]byte(fixture.raw))
			if receipt.Source.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("source hash mismatch")
			}
			var chunks int
			if err = s.DB.QueryRow(`SELECT COUNT(*) FROM external_source_chunks WHERE source_id=?`, receipt.Source.ID).Scan(&chunks); err != nil {
				t.Fatal(err)
			}
			if chunks != (len(fixture.raw)+externalChunkSize-1)/externalChunkSize {
				t.Fatalf("chunk count=%d", chunks)
			}
			rows, err := s.Session(context.Background(), receipt.SessionIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[1].StatusCode != 0 || rows[1].Outcome != "imported" || rows[1].SourceFormat != "external-native-event" {
				t.Fatalf("projection=%+v", rows)
			}
			var decoded struct {
				Event ExternalEvent `json:"external_event"`
			}
			if err = json.Unmarshal(rows[1].OriginalRequest, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Event.Role != session.Events[1].Role || !bytes.Contains(decoded.Event.Data, []byte(`"encrypted_content":"opaque-native-value"`)) || !bytes.Contains(decoded.Event.Data, []byte("9007199254740993")) {
				t.Fatalf("native data lost: %s", rows[1].OriginalRequest)
			}
			if _, ok := rows[1].Facets["thread.id"]; ok {
				t.Fatal("native ID must not become repairable thread.id")
			}
		})
	}
}

func TestExternalReplayAppendRevisionRestartAndGC(t *testing.T) {
	s, path := externalTestStore(t)
	ctx := context.Background()
	source, session := externalTestSource(), externalTestSession()
	first, err := s.ImportExternal(ctx, source, strings.NewReader("revision one\n"), []ExternalSession{session})
	if err != nil {
		t.Fatal(err)
	}
	source.ModifiedAt = source.ModifiedAt.Add(time.Hour)
	replay, err := s.ImportExternal(ctx, source, strings.NewReader("revision one\n"), []ExternalSession{session})
	if err != nil || !replay.Replayed || replay.ImportedEvents != 0 || replay.Source != first.Source {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	session.Events = append(session.Events, ExternalEvent{ID: "assistant-2", Role: "assistant", Text: "Appended", Timestamp: source.ModifiedAt})
	revision, err := s.ImportExternal(ctx, source, strings.NewReader("revision two\n"), []ExternalSession{session})
	if err != nil || revision.Replayed || revision.ImportedEvents != 1 || revision.SessionIDs[0] != first.SessionIDs[0] || revision.Source.ID == first.Source.ID {
		t.Fatalf("revision=%+v err=%v", revision, err)
	}
	listed, err := s.SessionsFiltered(ctx, 10, map[string]string{"source.kind": "external-local-history", "source.account": "minoo"})
	if err != nil || len(listed) != 1 || listed[0].Requests != 3 {
		t.Fatalf("sessions=%+v err=%v", listed, err)
	}
	if repaired, err := s.RepairCanonicalSessions(ctx); err != nil || repaired != 0 {
		t.Fatalf("repair=%d err=%v", repaired, err)
	}
	if _, err = s.GC(); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	sources, err := reopened.ExternalSources(ctx, first.SessionIDs[0])
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	for _, want := range []struct{ id, raw string }{{first.Source.ID, "revision one\n"}, {revision.Source.ID, "revision two\n"}} {
		var out bytes.Buffer
		if err = reopened.WriteExternalSource(ctx, want.id, &out); err != nil || out.String() != want.raw {
			t.Fatalf("raw=%q err=%v", out.String(), err)
		}
	}
}

func TestExternalNamespaceAndAtomicConflict(t *testing.T) {
	s, _ := externalTestStore(t)
	ctx := context.Background()
	source, session := externalTestSource(), externalTestSession()
	first, err := s.ImportExternal(ctx, source, strings.NewReader("original"), []ExternalSession{session})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ExternalSource){
		func(s *ExternalSource) { s.Host = "other-host" },
		func(s *ExternalSource) { s.Account = "other-account" },
		func(s *ExternalSource) { s.Tool = "other-tool" },
	} {
		other := source
		change(&other)
		receipt, err := s.ImportExternal(ctx, other, strings.NewReader("original"), []ExternalSession{session})
		if err != nil || receipt.SessionIDs[0] == first.SessionIDs[0] {
			t.Fatalf("namespace collision: %+v %v", receipt, err)
		}
	}
	before := externalTableCounts(t, s)
	session.Events = nil
	for i := 0; i < 128; i++ {
		session.Events = append(session.Events, ExternalEvent{ID: "new-before-conflict-" + strconv.Itoa(i), Role: "user", Text: "Must roll back"})
	}
	session.Events = append(session.Events, ExternalEvent{ID: "user-1", Role: "user", Text: "Conflicting changed event"})
	_, err = s.ImportExternal(ctx, source, strings.NewReader("new conflicting revision"), []ExternalSession{session})
	if err == nil || !strings.Contains(err.Error(), "content conflict") {
		t.Fatalf("expected content conflict: %v", err)
	}
	if after := externalTableCounts(t, s); after != before {
		t.Fatalf("partial transaction before=%v after=%v", before, after)
	}
}

func externalTableCounts(t *testing.T, s *Store) [12]int {
	t.Helper()
	var counts [12]int
	for i, table := range []string{"external_sources", "external_chunks", "external_events", "external_session_sources", "records", "turn_records", "blobs", "session_indexed_requests", "external_sessions", "external_source_chunks", "session_summaries", "session_facets"} {
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

type failingExternalReader struct{ sent bool }

func (r *failingExternalReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial file bytes"), nil
	}
	return 0, errors.New("source read failed")
}

func TestExternalReadFailureAndInvalidEventRollback(t *testing.T) {
	s, _ := externalTestStore(t)
	before := externalTableCounts(t, s)
	_, err := s.ImportExternal(context.Background(), externalTestSource(), &failingExternalReader{}, []ExternalSession{externalTestSession()})
	if err == nil || !strings.Contains(err.Error(), "source read failed") {
		t.Fatalf("reader error=%v", err)
	}
	session := externalTestSession()
	session.Events[1].Data = json.RawMessage(`{invalid`)
	_, err = s.ImportExternal(context.Background(), externalTestSource(), strings.NewReader("raw"), []ExternalSession{session})
	if err == nil {
		t.Fatal("expected invalid event failure")
	}
	if after := externalTableCounts(t, s); after != before {
		t.Fatalf("partial failure state=%v", after)
	}
	if err = s.WriteExternalSource(context.Background(), "unknown-source", io.Discard); err == nil {
		t.Fatal("unknown source must fail")
	}
}

func TestExternalRawOnlyInventory(t *testing.T) {
	s, _ := externalTestStore(t)
	ctx := context.Background()
	receipt, err := s.ImportExternal(ctx, externalTestSource(), strings.NewReader("raw-only artifact"), nil)
	if err != nil || receipt.Sessions != 0 || receipt.Events != 0 {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	sources, err := s.ExternalSources(ctx, "")
	if err != nil || len(sources) != 1 || sources[0] != receipt.Source {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	var out bytes.Buffer
	if err = s.WriteExternalSource(ctx, sources[0].ID, &out); err != nil || out.String() != "raw-only artifact" {
		t.Fatalf("raw=%q err=%v", out.String(), err)
	}
}
