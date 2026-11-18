package archive

import (
	"encoding/json"
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
	if reply.Result.SchemaVersion != 5 {
		t.Fatalf("schema_version = %d, want 5", reply.Result.SchemaVersion)
	}
}
