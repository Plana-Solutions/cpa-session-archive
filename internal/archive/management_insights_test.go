package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementInsightsRoute(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/insights" || r.URL.Query().Get("days") != "7" || r.URL.Query().Get("min_requests") != "2" {
			t.Errorf("proxy request = %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"window_days":7}`))
	}))
	defer collector.Close()
	p := &Plugin{endpoint: collector.URL + "/ingest", client: collector.Client()}
	found := false
	for _, route := range p.managementRegistration()["Routes"].([]map[string]string) {
		if route["Method"] == "GET" && route["Path"] == "/plugins/cpa-session-archive/insights" {
			found = true
		}
	}
	if !found {
		t.Fatal("insights route not registered")
	}
	raw, err := p.handleManagement([]byte(`{"Method":"GET","Path":"/v0/management/plugins/cpa-session-archive/insights","Query":{"days":["7"],"min_requests":["2"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct{ Result managementResponse }
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Result.StatusCode != 200 || string(reply.Result.Body) != `{"window_days":7}` {
		t.Fatalf("reply = %s", raw)
	}
}
