package archive

import (
	"bytes"
	"testing"
)

func TestManagementUIContainsStandaloneFleethouseApp(t *testing.T) {
	checks := [][]byte{
		[]byte("enc::v2::"),
		[]byte("enc::v1::"),
		[]byte("cli-proxy-auth"),
		[]byte("Fleethouse"),
		[]byte("ClickHouse"),
		[]byte("/v0/management/plugins/cpa-session-archive"),
		[]byte("/cancel-search"),
		[]byte("/request-context"),
		[]byte("/request-view"),
		[]byte("/turn-text"),
		[]byte("/export"),
		[]byte("Standalone"),
	}
	for _, check := range checks {
		if !bytes.Contains(managementHTML, check) {
			t.Errorf("management UI missing %q", check)
		}
	}
	for _, forbidden := range [][]byte{[]byte("sk-live-"), []byte("cpamp_actual_secret_")} {
		if bytes.Contains(managementHTML, forbidden) {
			t.Errorf("management UI contains credential-like literal %q", forbidden)
		}
	}
	if bytes.Contains(managementHTML, []byte("response.blob()")) {
		t.Error("complete exports must not be buffered in the browser")
	}
}
