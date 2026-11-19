package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestArchiveFixture(t *testing.T) {
	var input []struct {
		ID      string
		Body    []byte
		Dedup   bool
		Lineage string
	}
	b, e := os.ReadFile(os.Getenv("ARCHIVE_SINK_INPUT"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &input); e != nil {
		t.Fatal(e)
	}
	s := newSink(sinkConfig{URL: os.Getenv("ARCHIVE_SINK_URL"), Database: "cliproxy", StoreBodies: true})
	if e = s.ensureSchema(s.cfg); e != nil {
		t.Fatal(e)
	}
	s.schemaOK = true
	for i, r := range input {
		s.cfg.BodyDedup = r.Dedup
		at := formatTime(time.Now().UTC().Add(-time.Hour).Add(time.Duration(i) * time.Second))
		s.addRequest(requestRow{RequestID: r.ID, TraceID: r.ID, StartedAt: at, CompletedAt: at, RequestHeaders: map[string]string{"session-id": r.Lineage}, RequestBody: string(r.Body), ResponseBody: `{"text":"answer"}`, Model: "fixture", StatusCode: 200, Outcome: "succeeded"})
		s.flushNow()
		if s.buffered != 0 || s.bodyRetry != nil {
			t.Fatalf("sink did not flush %s", r.ID)
		}
	}
}
