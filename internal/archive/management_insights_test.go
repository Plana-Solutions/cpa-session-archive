package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementInsightsRoute(t *testing.T) {
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Query().Get("param_from") == "" {
			t.Errorf("unbounded query")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}\n"))
	}))
	defer ch.Close()
	p := &Plugin{reader: &clickhouseReader{config: pluginConfig{URL: ch.URL, Database: "cliproxy"}, client: ch.Client()}}
	raw, err := p.handleManagement([]byte(`{"Method":"GET","Path":"/v0/management/plugins/cpa-session-archive/insights","Query":{"days":["7"],"min_requests":["2"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct{ Result managementResponse }
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Result.StatusCode != 200 {
		t.Fatalf("reply = %s", raw)
	}
}
