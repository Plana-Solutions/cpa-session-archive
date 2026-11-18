package archive

import (
	"encoding/json"
	"html"
	"strings"
	"testing"
)

// CPA sends the full original request with every stream chunk to plugins below schema 5,
// which made this plugin decode the whole request per chunk and pin a CPU core.
func TestRegisterIsManagementOnly(t *testing.T) {
	p := NewPlugin()
	defer p.Shutdown()
	raw, err := p.Handle("plugin.register", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			SchemaVersion int             `json:"schema_version"`
			Capabilities  map[string]bool `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Capabilities) != 1 || !reply.Result.Capabilities["management_api"] {
		t.Fatal("capture capabilities still registered")
	}
	if _, err := p.Handle("request.intercept_before", []byte(`{}`)); err == nil {
		t.Fatal("capture hook still accepted")
	}
	if reply.Result.SchemaVersion != 7 {
		t.Fatalf("schema_version = %d, want 7", reply.Result.SchemaVersion)
	}
}

// Reproduces v8.0.77 managementResponseEscapesHTML and htmlsanitize.JSONValue.
// https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.77/internal/pluginhost/management.go
func cpaHostManagementBody(t *testing.T, schema int, body []byte) []byte {
	t.Helper()
	if schema >= 6 {
		return body
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	var escape func(any) any
	escape = func(value any) any {
		switch v := value.(type) {
		case string:
			return html.EscapeString(v)
		case map[string]any:
			for key, item := range v {
				v[key] = escape(item)
			}
		case []any:
			for i, item := range v {
				v[i] = escape(item)
			}
		}
		return value
	}
	out, err := json.Marshal(escape(value))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExportJSONLSurvivesCPAHost(t *testing.T) {
	p := NewPlugin()
	defer p.Shutdown()
	raw, err := p.Handle("plugin.register", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var registration struct {
		Result struct {
			Schema int `json:"schema_version"`
		}
	}
	if err = json.Unmarshal(raw, &registration); err != nil {
		t.Fatal(err)
	}
	content := "{\"messages\":[{\"role\":\"user\",\"content\":\"literal &amp; <tag>\"}]}\n"
	body, _ := json.Marshal(map[string]string{"content": content})
	for _, schema := range []int{5, registration.Result.Schema} {
		var ticket struct{ Content string }
		if err = json.Unmarshal(cpaHostManagementBody(t, schema, body), &ticket); err != nil {
			t.Fatal(err)
		}
		if got := json.Valid([]byte(strings.TrimSpace(ticket.Content))); got != (schema >= 6) {
			t.Fatalf("schema %d parses=%v", schema, got)
		}
		if schema >= 6 && ticket.Content != content {
			t.Fatal("host changed export content")
		}
	}
}
