package archive

import (
	"encoding/json"
	"testing"
)

// CPA sends the full original request with every stream chunk to plugins below schema 5,
// which made this plugin decode the whole request per chunk and pin a CPU core.
func TestRegisterOptsOutOfPerChunkRequestBodies(t *testing.T) {
	p := NewPlugin()
	defer p.Shutdown()
	raw, err := p.Handle("plugin.register", []byte(`{"config_yaml":"endpoint: http://127.0.0.1:1\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			SchemaVersion int `json:"schema_version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Result.SchemaVersion != 5 {
		t.Fatalf("schema_version = %d, want 5", reply.Result.SchemaVersion)
	}
}
