package chimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestClientDisablesQueryConditionCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query()["use_query_condition_cache"]; len(got) != 1 || got[0] != "0" {
			t.Errorf("use_query_condition_cache = %v, want [0]", got)
		}
	}))
	defer server.Close()
	c := newClient(server.URL+"?use_query_condition_cache=1", "", "", "")
	for _, params := range []url.Values{nil, {"use_query_condition_cache": {"1"}}} {
		if _, err := c.call(context.Background(), "SELECT 1", params, nil, false); err != nil {
			t.Fatal(err)
		}
	}
}
