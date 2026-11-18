package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
