package archive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClickHouseReadBoundary(t *testing.T) {
	for _, mode := range []string{"error", "oversize", "partial"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "reader" || password != "test-private-password" {
					t.Error("missing reader authentication")
				}
				if strings.Contains(r.URL.String(), "password") {
					t.Error("credentials in URL")
				}
				switch mode {
				case "error":
					w.WriteHeader(500)
					w.Write([]byte("server leaked test-private-password"))
				case "oversize":
					w.Write([]byte(strings.Repeat("x", resultLimit+1)))
				case "partial":
					w.Write([]byte("{\"n\":1}\nCode: query failed"))
				}
			}))
			defer server.Close()
			c := &clickhouseReader{config: pluginConfig{URL: server.URL, Database: "cliproxy", User: "reader", Password: "test-private-password"}, client: server.Client()}
			var rows []map[string]any
			err := c.query(context.Background(), "SELECT 1", nil, &rows)
			if err == nil || len(rows) != 0 {
				t.Fatal("accepted incomplete/oversize/error response")
			}
			if strings.Contains(err.Error(), "test-private-password") {
				t.Fatal("server error exposed a credential")
			}
		})
	}
}
func TestReaderConfigAndDateValidation(t *testing.T) {
	t.Setenv("CLICKHOUSE_READER_PASSWORD", "fixture-env-password")
	p := NewPlugin()
	defer p.Shutdown()
	config, _ := json.Marshal(lifecycle{ConfigYAML: []byte("clickhouse-url: http://127.0.0.1:8123\n")})
	registration, err := p.configure(config)
	if err != nil {
		t.Fatal(err)
	}
	if p.reader.config.Password != "fixture-env-password" || p.reader.config.Database != "cliproxy" || p.reader.config.User != "inference_reader" {
		t.Fatal("defaults or env fallback ignored")
	}
	if strings.Contains(string(registration), "fixture-env-password") {
		t.Fatal("configuration password in registration")
	}
	config, _ = json.Marshal(lifecycle{ConfigYAML: []byte("clickhouse-url: https://reader:secret@example.com\n")})
	if _, err = p.configure(config); err == nil {
		t.Fatal("accepted credentials in endpoint URL")
	}
	for _, q := range []url.Values{{"from": {"2026-11-02"}, "to": {"2026-11-01"}}, {"days": {"367"}}, {"from": {"2025-01-01"}, "to": {"2026-01-02"}}} {
		if _, err = windowFor(q, 30); err == nil {
			t.Fatalf("accepted invalid range %v", q)
		}
	}
	w, err := windowFor(url.Values{"from": {"2026-11-01"}, "to": {"2026-11-01"}}, 30)
	if err != nil || w.to.Sub(w.from).Hours() != 24 {
		t.Fatal("inclusive UTC day failed", err)
	}
}

func TestReadCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	c := &clickhouseReader{config: pluginConfig{URL: server.URL, Database: "cliproxy"}, client: server.Client()}
	first := make(chan error, 1)
	go func() {
		_, err := c.readContext(context.Background(), "sessions", url.Values{"search_client": {"tab"}})
		first <- err
	}()
	<-started
	parent, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := c.readContext(parent, "sessions", url.Values{"search_client": {"tab"}})
		second <- err
	}()
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("superseded search succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old search not cancelled")
	}
	<-started
	_, err := c.read("cancel-search", url.Values{"search_client": {"tab"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("page exit search succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("page exit did not cancel")
	}
	cancel()
	parent, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.readContext(parent, "sessions", nil); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnected request succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel")
	}
}

func TestSearchQueryBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("max_execution_time") != "3" || q.Get("max_result_bytes") != "8388608" {
			t.Errorf("search limits %v", q)
		}
		w.Write([]byte("{}\n"))
	}))
	defer server.Close()
	c := &clickhouseReader{config: pluginConfig{URL: server.URL}, client: server.Client()}
	var rows []map[string]any
	if err := c.query(context.Background(), "SELECT 1", url.Values{"search": {"term"}}, &rows); err != nil {
		t.Fatal(err)
	}
}

func TestSearchTimeoutMessage(t *testing.T) {
	for _, header := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if header {
				w.Header().Set("X-ClickHouse-Exception-Code", "159")
			}
			w.WriteHeader(500)
			w.Write([]byte("Code: 159. DB::Exception: private query TIMEOUT_EXCEEDED"))
		}))
		c := &clickhouseReader{config: pluginConfig{URL: server.URL}, client: server.Client()}
		var rows []map[string]any
		err := c.query(context.Background(), "SELECT 1", url.Values{"search": {"term"}}, &rows)
		if err == nil || err.Error() != "search timed out, narrow the range" {
			t.Fatalf("error %v", err)
		}
		server.Close()
	}
}
