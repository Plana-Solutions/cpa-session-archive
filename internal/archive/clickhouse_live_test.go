package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type summaryTransport struct {
	base        http.RoundTripper
	rows, bytes uint64
}

func (s *summaryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := s.base.RoundTrip(r)
	if err == nil {
		var v struct {
			Rows  string `json:"read_rows"`
			Bytes string `json:"read_bytes"`
		}
		json.Unmarshal([]byte(resp.Header.Get("X-ClickHouse-Summary")), &v)
		var rows, bytes uint64
		fmt.Sscan(v.Rows, &rows)
		fmt.Sscan(v.Bytes, &bytes)
		s.rows += rows
		s.bytes += bytes
	}
	return resp, err
}
func TestClickHouseLiveEvidence(t *testing.T) {
	if os.Getenv("ARCHIVE_LIVE_EVIDENCE") == "" {
		t.Skip("explicit live evidence opt-in required")
	}
	s := &summaryTransport{base: http.DefaultTransport}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{Transport: s}}
	cutoff := os.Getenv("ARCHIVE_EVIDENCE_TO")
	if cutoff == "" {
		cutoff = time.Now().UTC().Format(time.RFC3339Nano)
	}
	params := url.Values{"cutoff": {cutoff}}
	var ids []struct {
		ID string `json:"id"`
		N  uint64 `json:"n"`
	}
	if err := c.query(context.Background(), `SELECT conversation_id AS id,count() AS n FROM cliproxy.request_facts WHERE started_at > parseDateTime64BestEffort({cutoff:String})-INTERVAL 365 DAY AND started_at < parseDateTime64BestEffort({cutoff:String}) AND startsWith(conversation_id,'claude:') GROUP BY conversation_id ORDER BY n DESC LIMIT 3`, params, &ids); err != nil {
		t.Fatal(err)
	}
	if len(ids) < 3 {
		t.Fatalf("need three conversations before cutoff, got %d", len(ids))
	}
	for _, v := range ids {
		out, err := c.read("turns", url.Values{"session_id": {v.ID}, "to": {cutoff}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		var p struct {
			Total int `json:"total"`
		}
		json.Unmarshal(b, &p)
		t.Logf("conversation=%s calls=%d turns=%d", v.ID, v.N, p.Total)
	}
	cases := []struct {
		name string
		q    url.Values
	}{{"sessions", nil}, {"sessions", url.Values{"q": {"fix"}}}, {"turns", url.Values{"session_id": {ids[0].ID}}}}
	var req []struct {
		ID string `json:"id"`
	}
	if err := c.query(context.Background(), `SELECT request_id AS id FROM cliproxy.request_facts WHERE started_at > now()-INTERVAL 365 DAY AND started_at < parseDateTime64BestEffort({cutoff:String}) ORDER BY started_at DESC LIMIT 1`, params, &req); err != nil {
		t.Fatal(err)
	}
	if len(req) == 0 {
		t.Fatal("no request in evidence window")
	}
	cases = append(cases, struct {
		name string
		q    url.Values
	}{"request-view", url.Values{"id": {req[0].ID}}})
	for _, v := range cases {
		if v.q == nil {
			v.q = url.Values{}
		}
		v.q.Set("to", cutoff)
		s.rows = 0
		s.bytes = 0
		_, err := c.read(v.name, v.q)
		t.Logf("endpoint=%s search=%t read_rows=%d read_bytes=%d error=%v", v.name, v.q.Get("q") != "", s.rows, s.bytes, err)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClickHouseLargestLiveExport(t *testing.T) {
	if os.Getenv("ARCHIVE_LIVE_EXPORT") == "" {
		t.Skip("explicit live export opt-in required")
	}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{Timeout: 60 * time.Second}}
	var rows []struct {
		ID    string `json:"id"`
		Bytes uint64 `json:"bytes"`
	}
	err := c.query(context.Background(), `SELECT f.conversation_id AS id,sum(length(r.request_body)+length(r.response_body)+arraySum(arrayMap(x->length(x),r.response_chunks))) AS bytes FROM cliproxy.request_facts AS f INNER JOIN cliproxy.requests AS r USING request_id WHERE f.started_at>now()-INTERVAL 365 DAY AND r.started_at>now()-INTERVAL 365 DAY AND f.conversation_id!='' GROUP BY f.conversation_id ORDER BY bytes DESC LIMIT 1`, nil, &rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"archive", "sft"} {
		start := time.Now()
		content, pages := collectExport(t, c, url.Values{"id": {rows[0].ID}, "format": {format}})
		t.Logf("largest_session=%s raw_bytes=%d format=%s export_bytes=%d pages=%d elapsed=%s valid_jsonl=true", rows[0].ID, rows[0].Bytes, format, len(content), pages, time.Since(start))
	}
}

type liveRound4Transport struct{ t *testing.T }

func (s liveRound4Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	start := time.Now()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || time.Since(start) > time.Second {
		s.t.Logf("live query seconds=%.2f failed=%t sql=%.200s", time.Since(start).Seconds(), err != nil, b)
	}
	return resp, err
}
func TestClickHouseLiveRound4(t *testing.T) {
	if os.Getenv("ARCHIVE_LIVE_ROUND4") == "" {
		t.Skip("explicit read-only live check opt-in required")
	}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{Timeout: 60 * time.Second, Transport: liveRound4Transport{t}}}
	var sessions []struct {
		ID string `json:"id"`
		N  uint64 `json:"n"`
	}
	if err := c.query(context.Background(), `SELECT conversation_id AS id,count() AS n FROM cliproxy.request_facts WHERE started_at>now()-INTERVAL 365 DAY AND conversation_id!='' AND `+conversationTraffic+` GROUP BY conversation_id ORDER BY n DESC LIMIT 1`, nil, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatal("no live session")
	}
	var requests []struct {
		ID, SHA string
		Bytes   uint64
	}
	if err := c.query(context.Background(), `SELECT request_id AS ID,hex(SHA256(request_body)) AS SHA,length(request_body) AS Bytes FROM cliproxy.requests WHERE started_at>now()-INTERVAL 365 DAY AND request_id IN (SELECT request_id FROM cliproxy.request_facts WHERE started_at>now()-INTERVAL 365 DAY AND conversation_id={session:String}) ORDER BY length(request_body) DESC LIMIT 1 SETTINGS max_threads=1,max_block_size=1`, url.Values{"session": {sessions[0].ID}}, &requests); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatal("no live request")
	}
	got, err := c.read("requests", url.Values{"id": {requests[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	body := got.(Record).OriginalRequest
	sum := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), requests[0].SHA) || uint64(len(body)) != requests[0].Bytes {
		t.Fatal("live request bytes differ")
	}
	t.Logf("detail full-body bytes=%d sha256_equal=true calls=%d", len(body), sessions[0].N)
	if _, err = c.read("request-context", url.Values{"id": {requests[0].ID}, "call_id": {"round4-nonexistent-call"}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []url.Values{
		{"id": {sessions[0].ID}, "format": {"archive"}},
		{"id": {sessions[0].ID}, "format": {"sft"}},
		{"scope": {"all"}, "format": {"sft"}},
	} {
		content, pages := collectExport(t, c, q)
		if len(content) == 0 {
			t.Fatal("empty live export")
		}
		t.Logf("live export format=%s scope=%s bytes=%d pages=%d valid_jsonl=true", q.Get("format"), q.Get("scope"), len(content), pages)
	}
}
