package archive

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestExternalMaintenancePreservesNativeProjectionAndRawSource(t *testing.T) {
	s, _ := externalTestStore(t)
	ctx := context.Background()
	raw := "event: response.created\ndata: {\"encrypted_content\":\"first\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"encrypted_content\":\"last\"}\n\n"
	receipt, err := s.ImportExternal(ctx, externalTestSource(), strings.NewReader(raw), []ExternalSession{externalTestSession()})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.SessionIDs[0]
	before, err := s.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	refs := func() []string {
		t.Helper()
		rows, err := s.DB.Query(`SELECT original_ref FROM records WHERE session_id=? ORDER BY request_id`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				t.Fatal(err)
			}
			out = append(out, ref)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	beforeRefs := refs()
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"normalize SSE", func() error { return s.NormalizeHistoricalSSE(ctx) }},
		{"repair previews", func() error { return s.RepairRecordPreviews(ctx) }},
		{"repair summaries", func() error { return s.RepairSessionSummaries(ctx) }},
		{"session index", func() error { return s.BackfillSessionIndex(ctx) }},
		{"turn projection", func() error { return s.BackfillTurnProjection(ctx, 64, 0) }},
		{"turn facets", func() error { return s.BackfillTurnFacetProjection(ctx, 64, 0) }},
		{"canonical repair", func() error { _, err := s.RepairCanonicalSessions(ctx); return err }},
		{"garbage collection", func() error { _, err := s.GC(); return err }},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	s.MigrateLegacy()
	after, err := s.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRefs, refs()) {
		t.Fatal("maintenance rewrote external event data, provenance or projection")
	}
	var out bytes.Buffer
	if err = s.WriteExternalSource(ctx, receipt.Source.ID, &out); err != nil || out.String() != raw {
		t.Fatalf("raw source changed: %q err=%v", out.String(), err)
	}
}
