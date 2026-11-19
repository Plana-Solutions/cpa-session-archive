package chimport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestBadInsertRequiresDataException(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   bool
	}{
		{400, "27", true}, {400, "117", true}, {400, "72", true},
		{400, "8", false}, {400, "10", false}, {400, "497", false}, {400, "47", false}, {400, "60", false}, {400, "241", false},
		{400, "", false}, {401, "27", false}, {403, "27", false}, {404, "27", false}, {429, "27", false},
	} {
		if got := badInsert(httpError{tc.status, tc.code}); got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}

func TestRunIDDurableAndFresh(t *testing.T) {
	dir := t.TempDir()
	store, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	o := Options{SQLite: store.DBPath, State: filepath.Join(dir, "state")}
	first, err := loadState(o, store.DB)
	if err != nil || first.RunID == "" {
		t.Fatal(err)
	}
	resumed, err := loadState(o, store.DB)
	if err != nil || resumed.RunID != first.RunID {
		t.Fatal("resume changed run ID", err)
	}
	o.State = filepath.Join(dir, "fresh")
	fresh, err := loadState(o, store.DB)
	if err != nil || fresh.RunID == first.RunID {
		t.Fatal("fresh state reused run ID", err)
	}
	fresh.RunID = ""
	save(o.State, fresh)
	if _, err = loadState(o, store.DB); err == nil {
		t.Fatal("legacy state accepted")
	}
}

func TestSchemaAndGrantRefusals(t *testing.T) {
	for _, table := range []string{"requests", "body_blobs", "external_messages"} {
		t.Run(table, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Query().Get("query"), "cliproxy."+table+" ") {
					w.Header().Set("X-ClickHouse-Exception-Code", "47")
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			err := newClient(server.URL, "", "", "").schemaPreflight(context.Background())
			if err == nil || !strings.Contains(err.Error(), "refused cliproxy."+table) {
				t.Fatal(err)
			}
		})
	}
	for _, table := range []string{"disks", "parts"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Query().Get("query"), "system."+table) {
				w.Header().Set("X-ClickHouse-Exception-Code", "497")
				w.WriteHeader(403)
			} else {
				io.WriteString(w, `{"free":99999999999}`)
			}
		}))
		err := newClient(server.URL, "", "", "").diskStatus(context.Background(), 0, io.Discard)
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "GRANT SELECT(") || !strings.Contains(err.Error(), "ON system."+table) {
			t.Fatal(err)
		}
	}
	if err := Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "--dedup-final") {
		t.Fatal(err)
	}
}

func TestUnknownBlobEncodingQuarantines(t *testing.T) {
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	a := fixture(t)
	store.PutBatch([]archive.Record{a})
	manifest := `{"x":{"$cpa_blob":"child","encoding":"future"}}`
	store.DB.Exec("INSERT INTO blobs(hash,raw_size,codec,data) VALUES('child',1,'raw',X'78'),('manifest',?,'raw',?)", len(manifest), []byte(manifest))
	store.DB.Exec("UPDATE records SET original_ref='manifest'")
	_, err = readArchive(context.Background(), store.DB, a.RequestID)
	if !quarantinable(err) || err.Error() != "unknown blob encoding" {
		t.Fatal(err)
	}
}

func TestStopFinishesBatchAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.sqlite")
	store, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	store.PutBatch([]archive.Record{fixture(t)})
	store.DB.Close()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("api-keys: []"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	usage := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "system.disks") {
			io.WriteString(w, `{"free":99999999999}`)
		}
		if strings.HasPrefix(q, "INSERT INTO cliproxy.requests") {
			cancel()
		}
		if strings.HasPrefix(q, "INSERT INTO cliproxy.usage") {
			usage = true
		}
	}))
	defer server.Close()
	o := Options{DedupFinal: true, SQLite: path, State: filepath.Join(dir, "state"), URL: server.URL, Config: cfg, AuthDir: dir, Batch: 16, ExternalSessions: -1}
	if err = Run(ctx, o); err != context.Canceled {
		t.Fatal(err)
	}
	var cp checkpoint
	b, _ := os.ReadFile(o.State)
	json.Unmarshal(b, &cp)
	if cp.LastID != 1 || !usage {
		t.Fatal("stop interrupted batch or lost checkpoint", cp.LastID, usage)
	}
}

func TestRollbackFreshStateIntegration(t *testing.T) {
	endpoint := os.Getenv("CHIMPORT_TEST_URL")
	if endpoint == "" {
		t.Skip("isolated ClickHouse required")
	}
	ctx := context.Background()
	c := newClient(endpoint, "default", "", rand.Text())
	host, err := c.call(ctx, "SELECT hostName() FORMAT TabSeparated", nil, nil, false)
	if err != nil || string(host) != "cpa-archive-import-sample\n" {
		t.Fatal("refusing non-sample server", err)
	}
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	o := Options{SQLite: store.DBPath, State: filepath.Join(t.TempDir(), "first.state")}
	cp, err := loadState(o, store.DB)
	if err != nil {
		t.Fatal(err)
	}
	c.runID = cp.RunID
	id := "rollback-" + rand.Text()
	row := []byte(`{"request_id":"` + id + `","requested_at":"2026-11-18 12:00:00","source":"archive-v1"}` + "\n")
	write := func() {
		t.Helper()
		if _, err := c.insert(ctx, "usage", [][]byte{row}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() string {
		t.Helper()
		b, err := c.call(ctx, "SELECT count() FROM cliproxy.usage WHERE request_id={id:String} FORMAT TabSeparated", url.Values{"param_id": {id}}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	remove := func() {
		t.Helper()
		if _, err := c.call(ctx, "DELETE FROM cliproxy.usage WHERE request_id={id:String} SETTINGS lightweight_deletes_sync=2", url.Values{"param_id": {id}}, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	defer remove()
	write()
	if count() != "1\n" {
		t.Fatal("initial insert absent")
	}
	remove()
	write()
	if count() != "0\n" {
		t.Fatal("test did not reproduce retained ClickHouse token")
	}
	o.State = filepath.Join(t.TempDir(), "fresh.state")
	cp, err = loadState(o, store.DB)
	if err != nil {
		t.Fatal(err)
	}
	c.runID = cp.RunID
	write()
	if count() != "1\n" {
		t.Fatal("fresh-state re-import silently deduplicated")
	}
}
