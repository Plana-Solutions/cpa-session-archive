package chimport

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/deriver/extract"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
	"github.com/mattn/go-sqlite3"
)

func TestFacetCodexIdentity(t *testing.T) {
	r, _ := redact.New()
	for _, kind := range []string{"turn", "compact", "review"} {
		a := fixture(t)
		a.Facets = map[string][]string{"originator": {"codex_cli_rs"}, "thread.id": {"t1"}, "request.kind": {kind}, "window.id": {"w1"}}
		row, _, err := mapRecord(a, r)
		if err != nil {
			t.Fatal(err)
		}
		f := extract.Extract(extractRow(row), false)
		want := "codex:t1"
		if kind != "turn" {
			want += ":" + kind
		}
		if f.ConversationID != want || row.RequestHeaders["x-codex-window-id"] != "w1" {
			t.Fatal(f.ConversationID)
		}
	}
}

func TestBodyRedactedBeforeSplit(t *testing.T) {
	secret := "private-known-key-12345678901234567890"
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: ["+secret+"]"), 0600)
	r, _ := redact.New()
	r.Configure([]string{cfg}, nil)
	a := fixture(t)
	a.SourceFormat = "claude"
	a.Metadata["request_path"] = "/v1/messages"
	a.OriginalRequest = []byte(`{ "messages": [ {"role":"user","content":"` + secret + `"}],"tail":"<&>" }`)
	row, _, err := mapRecord(a, r)
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte(row.RequestBody)
	b := splitBody(&row, a.SessionID)
	got, err := bodystore.Join(b.sk, b.blobs)
	if err != nil || !bytes.Equal(got, expected) || bytes.Contains(got, []byte(secret)) || row.RequestBody != string(expected) {
		t.Fatal("body roundtrip/redaction")
	}
	f := extract.Extract(extractRow(row), false)
	if len(f.Messages) == 0 || f.LastUserText == "" {
		t.Fatal("deriver lost inline body after split")
	}
	for _, blob := range b.blobs {
		if bytes.Contains(blob, []byte(secret)) {
			t.Fatal("unredacted blob")
		}
	}
}

func TestSourceCorruptionAndFatalErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a := fixture(t)
	s.PutBatch([]archive.Record{a})
	if _, err = readArchive(context.Background(), s.DB, a.RequestID); err != nil {
		t.Fatal(err)
	}
	s.DB.Exec("UPDATE records SET original_ref='',original_request_gz=? WHERE request_id=?", []byte("broken"), a.RequestID)
	_, err = readArchive(context.Background(), s.DB, a.RequestID)
	if !quarantinable(err) {
		t.Fatal("corrupt gzip not quarantinable", err)
	}
	for _, e := range []error{sqlite3.Error{Code: sqlite3.ErrIoErr}, sqlite3.Error{Code: sqlite3.ErrNomem}, context.Canceled} {
		if quarantinable(e) {
			t.Fatal("fatal error quarantined", e)
		}
	}
	if !quarantinable(sql.ErrNoRows) {
		t.Fatal("missing blob must quarantine")
	}
	s.DB.Exec("UPDATE records SET started_at=NULL WHERE request_id=?", a.RequestID)
	_, err = readArchive(context.Background(), s.DB, a.RequestID)
	if !quarantinable(err) {
		t.Fatal("NULL timestamp must quarantine", err)
	}
	s.DB.Exec("UPDATE records SET started_at=?,metadata_json='{' WHERE request_id=?", a.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), a.RequestID)
	_, err = readArchive(context.Background(), s.DB, a.RequestID)
	if !quarantinable(err) {
		t.Fatal("JSON not quarantinable")
	}
}

func TestSplitBadInsertAndExceptionCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := readInsert(t, r)
		for _, raw := range rows {
			if bytes.Contains(raw, []byte("bad")) {
				w.Header().Set("X-ClickHouse-Exception-Code", "27")
				w.WriteHeader(400)
				return
			}
		}
	}))
	defer server.Close()
	rows := [][]byte{[]byte("{\"v\":\"one\"}\n"), []byte("{\"v\":\"bad\"}\n"), []byte("{\"v\":\"two\"}\n")}
	quarantined := map[int64]string{}
	n, _, err := newClient(server.URL, "", "", "test-run").insertSplit(context.Background(), "usage", rows, []int64{1, 2, 3}, func(id int64, why string) error { quarantined[id] = why; return nil })
	if err != nil || n != 2 || len(quarantined) != 1 || !strings.Contains(quarantined[2], `exception_code="27"`) {
		t.Fatal(n, err, quarantined)
	}
}

func TestPartsGuardAndUnreservedSpace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "system.disks") {
			if !strings.Contains(q, "unreserved_space") {
				t.Error("wrong disk space")
			}
			io.WriteString(w, `{"free":999999999999}`)
		} else {
			io.WriteString(w, `{"table":"requests","partition":"202611","parts":301}`)
		}
	}))
	defer server.Close()
	if err := newClient(server.URL, "", "", "test-run").diskStatus(context.Background(), 0, io.Discard); err == nil {
		t.Fatal("parts guard did not stop")
	}
}

func TestJSONDoesNotEscapeHTML(t *testing.T) {
	b, err := encodeJSON(map[string]string{"text": "<&>"})
	if err != nil || !bytes.Contains(b, []byte("<&>")) || !json.Valid(b) {
		t.Fatal("HTML escaped")
	}
}

func TestResponseChunksPreserveBytes(t *testing.T) {
	for _, body := range []string{"data: one\r\n\r\ndata: two\r\n\r\n", "data: one\n\ndata: two\n\n", "raw\x00\xff"} {
		if strings.Join(splitStream(body), "") != body {
			t.Fatal("response bytes changed")
		}
	}
}

func TestQuarantineBreakerKeepsCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	s.PutBatch([]archive.Record{a})
	s.DB.Exec("UPDATE records SET completed_at='invalid'")
	s.DB.Close()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: []"), 0600)
	inserts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "system.disks") {
			io.WriteString(w, `{"free":999999999999}`)
		}
		if strings.HasPrefix(q, "INSERT") {
			inserts++
		}
	}))
	defer server.Close()
	o := Options{SQLite: path, State: filepath.Join(dir, "state"), URL: server.URL, Config: cfg, AuthDir: dir, Batch: 16, ExternalSessions: -1, MaxQuarantined: 1}
	if err = Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatal(err)
	}
	ro, _ := openReadOnly(path)
	defer ro.DB.Close()
	state, err := loadState(o, ro.DB)
	if err != nil || state.LastID != 0 || len(state.Quarantine) != 1 || inserts != 0 {
		t.Fatal("breaker advanced checkpoint", state, err)
	}
	fix, _ := sql.Open("sqlite3", path)
	fix.Exec("UPDATE records SET completed_at=?", a.CompletedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fix.Close()
	o.RetryQuarantined = true
	if err = Run(context.Background(), o); err != nil {
		t.Fatal("retry", err)
	}
	state, err = loadState(o, ro.DB)
	if err != nil || state.LastID != 0 || len(state.Quarantine) != 0 {
		t.Fatal("retry must preserve main frontier", state, err)
	}

}

func TestBodyPreloadIntegration(t *testing.T) {
	endpoint := os.Getenv("CHIMPORT_TEST_URL")
	if endpoint == "" {
		t.Skip("isolated ClickHouse required")
	}
	c := newClient(endpoint, "default", "", rand.Text())
	ctx := context.Background()
	host, err := c.call(ctx, "SELECT hostName() FORMAT TabSeparated", nil, nil, false)
	if err != nil || string(host) != "cpa-archive-import-sample\n" {
		t.Fatal("refusing non-sample server", err)
	}
	row := requestRow{RequestBody: `{"messages":[{"content":"preload regression"}]}`, StartedAt: "2026-11-18 12:00:00.000000000"}
	item := splitBody(&row, "chimport-preload-regression")
	defer c.call(ctx, "DELETE FROM cliproxy.body_blobs WHERE lineage={lineage:UInt64} SETTINGS lightweight_deletes_sync=2", url.Values{"param_lineage": {strconv.FormatUint(item.lineage, 10)}}, nil, false)
	if n, err := c.writeBodies(ctx, []bodyItems{item}); err != nil || n == 0 {
		t.Fatal("initial blob", n, err)
	}
	if n, err := c.writeBodies(ctx, []bodyItems{item}); err != nil || n != 0 {
		t.Fatal("committed blob was inserted again", n, err)
	}
}

func TestHTTPResponseLimitIsExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "12345") }))
	defer server.Close()
	c := newClient(server.URL, "", "", "test-run")
	c.responseLimit = 4
	if b, err := c.call(context.Background(), "SELECT 1", nil, nil, false); err == nil || b != nil {
		t.Fatal("silently truncated response", err)
	}
}

func TestVerifySampleRejectsEmptyDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	b := a
	b.RequestID = "req2"
	s.PutBatch([]archive.Record{a, b})
	s.DB.Close()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: []"), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "hostName()") {
			io.WriteString(w, "cpa-archive-import-sample\n")
		}
	}))
	defer server.Close()
	state := filepath.Join(dir, "state")
	save(state, checkpoint{UpperID: 2})
	err = VerifySample(context.Background(), Options{URL: server.URL, SQLite: path, State: state, Config: cfg, AuthDir: dir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requests missing") {
		t.Fatal("empty sample accepted", err)
	}
}

func TestEmptyGzipBlobIsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	s, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a := fixture(t)
	s.PutBatch([]archive.Record{a})
	s.DB.Exec("INSERT INTO blobs(hash,raw_size,codec,data) VALUES('empty-broken',0,'gzip',X'')")
	s.DB.Exec("UPDATE records SET original_ref='empty-broken' WHERE request_id=?", a.RequestID)
	_, err = readArchive(context.Background(), s.DB, a.RequestID)
	if !quarantinable(err) || err.Error() != "corrupt gzip" {
		t.Fatal("empty compressed blob imported", err)
	}
}

func TestBlobPreloadBoundsAndDeduplicatesQueries(t *testing.T) {
	item := bodyItems{lineage: 7, blobs: map[bodystore.Hash][]byte{}}
	for i := 0; i < 600; i++ {
		b := []byte(strconv.Itoa(i))
		item.blobs[bodystore.HashBytes(b)] = b
	}
	queries := 0
	refs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.HasPrefix(q, "SELECT") {
			queries++
			refs += strings.Count(q, "unhex(")
			if len(r.URL.RequestURI()) > 16384 {
				t.Error("preload exceeds HTTP field budget")
				w.WriteHeader(500)
			}
		}
		io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	if _, err := newClient(server.URL, "", "", "test-run").writeBodies(context.Background(), []bodyItems{item, item}); err != nil {
		t.Fatal(err)
	}
	if queries < 2 || refs != 600 {
		t.Fatal("preload did not bound distinct references", queries, refs)
	}
}
