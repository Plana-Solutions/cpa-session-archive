package chimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/deriver/extract"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
	"github.com/mattn/go-sqlite3"
)

func TestLiveExtractionParity(t *testing.T) {
	r, _ := redact.New()
	for _, tc := range []struct {
		name, format, path, body, session, conversation, client, parent string
		headers                                                         map[string][]string
	}{
		{"claude", "claude", "/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`, "s1", "claude:s1", "claude-code", "", map[string][]string{"header.x-claude-code-session-id": {"s1"}, "client": {"claude-cli/2.1.319 (external, cli)"}, "client.user_agent": {"claude-cli/2.1.319 (external, cli)"}}},
		{"subagent", "claude", "/v1/messages", `{"metadata":{"user_id":"{\"session_id\":\"s1\",\"agent_id\":\"a7\"}"},"messages":[]}`, "s1", "claude:s1:agent:a7", "claude-code", "claude:s1", map[string][]string{"header.x-claude-code-session-id": {"s1"}}},
		{"codex", "openai-response", "/v1/responses", `{"client_metadata":{"thread_id":"t1"},"input":"hi"}`, "t1", "codex:t1", "codex", "", map[string][]string{"originator": {"codex"}}},
		{"count_tokens", "claude", "/v1/messages/count_tokens", `{"messages":[]}`, "request:req1", "claude:s1", "claude-code", "", map[string][]string{"header.x-claude-code-session-id": {"s1"}}},
		{"count_tokens_fingerprint", "claude", "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hi"}]}`, "request:req1", "fp:3affd2e367e55502", "claude-code", "", map[string][]string{"client.user_agent": {"claude-cli/2.1.319"}}},
		{"chat", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, "chat1", "fp:5748322fd07ae370", "OpenAI", "", map[string][]string{"client": {"OpenAI/JS 5.20.1"}, "client.user_agent": {"OpenAI/JS 5.20.1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixture(t)
			a.SourceFormat = tc.format
			a.Metadata["request_path"] = tc.path
			a.SessionID = tc.session
			a.OriginalRequest = []byte(tc.body)
			a.Facets = tc.headers
			row, u, err := mapRecord(a, r)
			if err != nil {
				t.Fatal(err)
			}
			f := extract.Extract(extractRow(row), false)
			if f.ConversationID != tc.conversation || f.Client != tc.client || f.ParentConversationID != tc.parent {
				t.Fatalf("conversation=%q client=%q parent=%q", f.ConversationID, f.Client, f.ParentConversationID)
			}
			if u.Generate != !strings.Contains(tc.path, "count_tokens") {
				t.Fatal("incorrect generate flag")
			}
		})
	}
	a := fixture(t)
	a.KeyID = strings.Repeat("b", 64)
	a.Metadata["caller_scope"] = a.KeyID
	a.Truncated = true
	row, u, _ := mapRecord(a, r)
	if u.APIKeyHash != "" || row.RequestHeaders["x-plana-archive-truncated"] != "true" {
		t.Fatal("key or truncation parity")
	}
	a.Metadata["key_hash"] = strings.Repeat("c", 64)
	_, u, _ = mapRecord(a, r)
	if u.APIKeyHash != strings.Repeat("c", 16) {
		t.Fatal("real key hash lost")
	}
}

func TestRedactionChunksHeadersErrors(t *testing.T) {
	r, _ := redact.New()
	secret := "private-key-value-12345678901234567890"
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("api-keys: ["+secret+"]"), 0600)
	r.Configure([]string{path}, nil)
	row := requestRow{RequestHeaders: map[string]string{"x-debug": secret}, ResponseHeaders: map[string]string{"x-debug": secret}, Error: secret, RequestBody: `{"x":"` + secret + `"}`, ResponseChunks: []string{secret[:18], secret[18:]}}
	redactRequest(r, &row)
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), secret) || strings.Contains(strings.Join(row.ResponseChunks, ""), secret) || row.Error == secret || row.RedactionCount == 0 {
		t.Fatal("redaction leaked")
	}
}

func TestRetries(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	c := newClient(server.URL, "", "", "test-run")
	b, err := c.call(context.Background(), "SELECT 1", nil, strings.NewReader("same body"), false)
	if err != nil || string(b) != "ok" || n != 3 {
		t.Fatal("transport retry", err, n)
	}
	n = 0
	err = retry(context.Background(), func() error {
		n++
		if n < 3 {
			return sqlite3.Error{Code: sqlite3.ErrBusy}
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatal("SQLite retry", err, n)
	}
}

func TestProxyRateLimit(t *testing.T) {
	runFixture(t, true)
}
func TestQuarantineByteCapAndNativeResume(t *testing.T) { runFixture(t, false) }

func runFixture(t *testing.T, pace bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	b := a
	b.RequestID = "bad"
	c := a
	c.RequestID = "no-model"
	c.Model = ""
	c.RequestedModel = ""
	d := a
	d.RequestID = "large"
	d.OriginalRequest = []byte(`{"input":"` + strings.Repeat("z", 8000) + `"}`)
	if err = s.PutBatch([]archive.Record{a, b, c, d}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE records SET original_ref='' WHERE request_id='req1'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE records SET started_at='invalid' WHERE request_id='bad'"); err != nil {
		t.Fatal(err)
	}
	_, err = s.ImportExternal(context.Background(), archive.ExternalSource{Tool: "codex", Host: "host", Account: "test", Format: "jsonl", Path: "native.jsonl"}, strings.NewReader("native source"), []archive.ExternalSession{{ID: "native1", Events: []archive.ExternalEvent{{Role: "user", Text: "one", Timestamp: a.StartedAt}, {Role: "assistant", Text: "two", Timestamp: a.StartedAt}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: []"), 0600)
	requests := map[string]bool{}
	usage := map[string]bool{}
	positions := map[uint32]int{}
	insertCalls, failNative := 0, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "system.disks"):
			io.WriteString(w, `{"free":100000000000}`)
			return
		case strings.Contains(q, "body_blobs"), strings.Contains(q, "system.parts"), strings.HasPrefix(q, "CREATE"), strings.HasPrefix(q, "ALTER"):
			return
		case strings.HasPrefix(q, "SELECT"):
			if strings.Contains(q, "cliproxy.requests") {
				for id := range requests {
					json.NewEncoder(w).Encode(map[string]any{"request_id": id, "imported": 1})
				}
			} else {
				for id := range usage {
					json.NewEncoder(w).Encode(map[string]string{"request_id": id})
				}
			}
			return
		}
		raw := readInsert(t, r)
		if strings.Contains(q, "external_messages") {
			var row externalRow
			json.Unmarshal(raw[0], &row)
			if row.Position == 1 && failNative {
				w.WriteHeader(500)
				return
			}
			for _, b := range raw {
				json.Unmarshal(b, &row)
				positions[row.Position]++
			}
			return
		}
		if strings.Contains(q, "cliproxy.requests") {
			insertCalls++
			if len(raw) != 1 {
				t.Error("byte cap combined records")
			}
			for _, b := range raw {
				var row requestRow
				json.Unmarshal(b, &row)
				requests[row.RequestID] = true
			}
		}
		if strings.Contains(q, "cliproxy.usage") {
			for _, b := range raw {
				var row usageRow
				json.Unmarshal(b, &row)
				usage[row.RequestID] = true
			}
		}
	}))
	defer server.Close()
	o := Options{SQLite: path, State: filepath.Join(dir, "state"), Config: cfg, AuthDir: dir, URL: server.URL, Batch: 16, BatchBytes: 3000, ExternalSessions: 1}
	if pace {
		o.Rate = 20
		o.ExternalSessions = -1
	}
	if !pace {
		o.Batch = 1
	}
	start := time.Now()
	err = Run(context.Background(), o)
	if pace {
		if err == nil || !strings.Contains(err.Error(), "quarantined") {
			t.Fatal("expected nonzero quarantine exit", err)
		}
		if time.Since(start) < 90*time.Millisecond {
			t.Fatal("rate cap did not throttle")
		}
	} else {
		if err == nil {
			t.Fatal("expected native failure")
		}
		ro, _ := openReadOnly(path)
		state, e := loadState(o, ro.DB)
		ro.DB.Close()
		if e != nil || state.NativePosition != 1 {
			t.Fatal("native failed batch checkpoint", e, state.NativePosition)
		}
		failNative = false
		o.Batch = 1
		o.Rate = 0.01
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err = Run(ctx, o); err == nil || !strings.Contains(err.Error(), "quarantined") {
			t.Fatal(err)
		}
		if err = Run(ctx, o); err == nil || !strings.Contains(err.Error(), "quarantined") {
			t.Fatal(err)
		}
		if positions[0] != 1 || positions[1] != 1 {
			t.Fatal("native resume duplicated positions", positions)
		}
	}
	ro, _ := openReadOnly(path)
	defer ro.DB.Close()
	state, err := loadState(o, ro.DB)
	if err != nil || state.NoModelSkipped != 1 || state.NoOriginalRef != 1 || len(state.Quarantine) != 1 || insertCalls != 2 || len(requests) != 2 || state.LastID != state.UpperID {
		t.Fatal("quarantine/skip/batch mismatch", err, state, insertCalls, len(requests))
	}
	fix, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fix.Exec("UPDATE records SET started_at=? WHERE request_id='bad'", a.StartedAt.Format(time.RFC3339Nano))
	fix.Close()
	if err != nil {
		t.Fatal(err)
	}
	o.RetryQuarantined = true
	o.Rate = 0
	if err = Run(context.Background(), o); err != nil {
		t.Fatal("retry quarantined", err)
	}
	state, err = loadState(o, ro.DB)
	if err != nil || len(state.Quarantine) != 0 || len(requests) != 3 {
		t.Fatal("retry did not repair only failed row", err, len(requests), state.Quarantine)
	}

}

func readInsert(t *testing.T, r *http.Request) []json.RawMessage {
	t.Helper()
	z, err := gzip.NewReader(r.Body)
	if err != nil {
		t.Error(err)
		return nil
	}
	defer z.Close()
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Error(err)
	}
	if strings.Contains(r.URL.Query().Get("query"), "FORMAT RowBinary") {
		return decodeRequests(t, raw)
	}
	var out []json.RawMessage
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		out = append(out, json.RawMessage(line))
	}
	return out
}

func TestDiskGuard(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Query().Get("query"), "system.disks") {
			io.WriteString(w, `{"free":100}`)
		}
	}))
	defer server.Close()
	var log bytes.Buffer
	if err := newClient(server.URL, "", "", "test-run").diskStatus(context.Background(), 101, &log); err == nil {
		t.Fatal("disk guard allowed low free space")
	}
	if calls != 2 || !strings.Contains(log.String(), "disk_free_bytes=100") {
		t.Fatal("missing disk/parts observation")
	}
}

func decodeRequests(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	rd := bytes.NewReader(raw)
	n := func() int {
		v, e := binary.ReadUvarint(rd)
		if e != nil {
			t.Fatal(e)
		}
		return int(v)
	}
	str := func() string {
		b := make([]byte, n())
		if _, e := io.ReadFull(rd, b); e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	strs := func() []string {
		out := []string{}
		for i := n(); i > 0; i-- {
			out = append(out, str())
		}
		return out
	}
	dict := func() map[string]string {
		out := map[string]string{}
		for i := n(); i > 0; i-- {
			k := str()
			out[k] = str()
		}
		return out
	}
	skip := func(n int) {
		if _, e := rd.Seek(int64(n), io.SeekCurrent); e != nil {
			t.Fatal(e)
		}
	}
	out := []json.RawMessage{}
	for rd.Len() > 0 {
		row := requestRow{RequestID: str(), TraceID: str()}
		skip(16)
		row.Path = str()
		row.SourceFormat = str()
		row.Model = str()
		row.RequestedModel = str()
		skip(1)
		row.Outcome = str()
		skip(2)
		row.Error = str()
		row.RequestHeaders = dict()
		row.RequestBody = str()
		row.ResponseHeaders = dict()
		row.ResponseBody = str()
		row.ResponseChunks = strs()
		skip(4)
		row.RedactionRules = strs()
		skip(8 + 4 + 32)
		str()
		skip(4 * n())
		skip(16 * n())
		b, _ := json.Marshal(row)
		out = append(out, b)
	}
	return out
}
