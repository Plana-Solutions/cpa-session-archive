package chimport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
	"github.com/zricethezav/gitleaks/v8/regexp"
)

func TestEmptyClassification(t *testing.T) {
	s, err := archive.OpenStore(filepath.Join(t.TempDir(), "source.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for _, tc := range []struct {
		id, path, model string
		body            []byte
		want            bool
	}{
		{"empty", "", "", nil, false},
		{"chat", "/v1/chat/completions", "", nil, true},
		{"body", "", "", []byte("{}"), true},
		{"model", "", "model", nil, true},
		{"aux", "/v1/messages/count_tokens", "", nil, false},
	} {
		_, err = s.DB.Exec("INSERT INTO records(request_id,session_id,source_format,model,metadata_json,original_request_gz) VALUES(?,?,'openai',?,?,?)", tc.id, tc.id, tc.model, `{"request_path":"`+tc.path+`"}`, tc.body)
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		if err = s.DB.QueryRow("SELECT "+importableProxyRows+" FROM records WHERE request_id=?", tc.id).Scan(&got); err != nil || got != tc.want {
			t.Fatalf("%s eligible=%v err=%v", tc.id, got, err)
		}
	}
	var empty, aux int
	s.DB.QueryRow("SELECT count(*) FROM records WHERE " + skippedEmptyRows).Scan(&empty)
	s.DB.QueryRow("SELECT count(*) FROM records WHERE " + skippedModelLessRows).Scan(&aux)
	if empty != 1 || aux != 1 {
		t.Fatalf("empty=%d auxiliary=%d", empty, aux)
	}
}

func TestOverlapWidened(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, "subtractDays(") || !strings.Contains(q, "addDays(") {
			t.Error("exact timestamp lookup", q)
		}
		io.WriteString(w, `{"request_id":"r","imported":0}`)
	}))
	defer server.Close()
	for _, table := range []string{"requests", "usage"} {
		got, err := newClient(server.URL, "", "", "").existing(context.Background(), table, []string{"r"}, "2026-11-18 00:00:00", "2026-11-18 00:00:00")
		if err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
	}
}

func TestRecordHeartbeat(t *testing.T) {
	var log bytes.Buffer
	stop := recordProgress(&log, 11, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	stop()
	if strings.Count(log.String(), "phase=prepare elapsed=") < 1 || !strings.Contains(log.String(), "phase=prepared") {
		t.Fatal(log.String())
	}
}

func TestImageBoundedAndUnchanged(t *testing.T) {
	if regexp.Version == "stdlib" {
		t.Skip("run with -tags gore2regex")
	}
	r, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("aB09", 8<<20) + `"}}]}]}`
	row := requestRow{RequestBody: body}
	start := time.Now()
	redactRequest(r, &row)
	if row.RequestBody != body || row.RedactionCount != 0 {
		t.Fatal("image bytes changed")
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("image scan exceeded 30 seconds")
	}
	t.Logf("image_bytes=%d elapsed=%s", len(body), time.Since(start))
}
