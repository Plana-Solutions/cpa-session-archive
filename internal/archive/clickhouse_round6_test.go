package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type round6Transport struct {
	summaryTransport
	queries int
}

func (s *round6Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.queries++
	return s.summaryTransport.RoundTrip(r)
}

func TestRound6SearchCost(t *testing.T) {
	if os.Getenv("ARCHIVE_ROUND6_COST") == "" {
		t.Skip("opt-in")
	}
	endpoint := os.Getenv("ARCHIVE_R6_URL")
	live := os.Getenv("ARCHIVE_R6_LIVE") != ""
	var c *clickhouseReader
	if endpoint == "" {
		c = localClickHouse(t)
		for _, file := range []string{"schema.sql", "derived.sql"} {
			b, _ := os.ReadFile("testdata/clickhouse/" + file)
			for _, sql := range strings.Split(string(b), "\n;\n") {
				chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
			}
		}
		for offset := 0; offset < 330000; offset += 2000 {
			chExec(t, c, fmt.Sprintf(`INSERT INTO cliproxy.request_facts (request_id,started_at,ingested_at,conversation_id,kind,client,prior_seen,input_items,last_user_text,msg_direction,msg_role,msg_type,msg_text) WITH intDiv(number,12) AS session,number%%12 AS step, toDateTime64('2026-11-18 16:00:00',9,'UTC')-toIntervalSecond((330000-number)*7) AS ts,concat('Please fix the common archive bug ',if(session=27490,'rarelyfoundneedle ',''),repeat(hex(cityHash64(session)),190)) AS prompt SELECT concat('req-',toString(number)),ts,ts,concat('claude:scale-',toString(session)),'messages','claude-code',step>0,200,prompt,['input'],[if(step=0,'user','tool')],['message'],[if(step=0,prompt,'tool output')] FROM numbers(%d,2000) SETTINGS max_threads=1,max_block_size=1000,min_insert_block_size_rows=1000,min_insert_block_size_bytes=0`, offset))
		}
		chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
		endpoint = c.config.URL
	} else {
		c = &clickhouseReader{config: pluginConfig{URL: endpoint, Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{}}
	}
	for i := 0; i < 5; i++ {
		if baseline := os.Getenv("ARCHIVE_R6_BASELINE"); baseline != "" {
			cmd := exec.Command(baseline, "-test.run=^TestRound6SearchCost$", "-test.v")
			cmd.Env = append(os.Environ(), "ARCHIVE_R6_URL="+endpoint, "ARCHIVE_R6_BASELINE=", "ARCHIVE_R6_RUNS=1")
			out, err := cmd.CombinedOutput()
			t.Logf("BASELINE %s", out)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, term := range []string{"absent-round6-needle", "rarelyfoundneedle", "common"} {
			s := &round6Transport{summaryTransport: summaryTransport{base: http.DefaultTransport}}
			c.client.Transport = s
			q := url.Values{"q": {term}, "from": {"2026-10-19T16:00:01Z"}, "to": {"2026-11-18T16:00:01Z"}, "show_system": {"1"}}
			if live {
				q.Set("from", os.Getenv("ARCHIVE_R6_FROM"))
				q.Set("to", os.Getenv("ARCHIVE_R6_TO"))
			}
			start := time.Now()
			out, err := c.read("sessions", q)
			n := 0
			if err == nil {
				n = len(out.([]archiveSession))
			}
			t.Logf("COST run=%d live=%t term=%s queries=%d rows=%d bytes=%d seconds=%.6f matches=%d error=%v", i, live, term, s.queries, s.rows, s.bytes, time.Since(start).Seconds(), n, err)
			if err != nil {
				t.Fatal(err)
			}
			if !live && ((term == "absent-round6-needle" && n != 0) || (term == "rarelyfoundneedle" && n != 1) || (term == "common" && n != 20)) {
				t.Fatal("wrong search output")
			}
		}
		if os.Getenv("ARCHIVE_R6_RUNS") == "1" {
			break
		}
	}
}

func TestRound6LiveTurns(t *testing.T) {
	if os.Getenv("ARCHIVE_R6_LIVE") == "" {
		t.Skip("opt-in")
	}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{}}
	w, _ := windowFor(url.Values{"from": {os.Getenv("ARCHIVE_R6_FROM")}, "to": {os.Getenv("ARCHIVE_R6_TO")}}, 30)
	var ids []struct {
		ID string `json:"id"`
	}
	err := c.query(context.Background(), `SELECT DISTINCT conversation_id AS id FROM (SELECT conversation_id,prior_seen,row_number() OVER (PARTITION BY conversation_id ORDER BY started_at,request_id) AS rn FROM cliproxy.request_facts WHERE `+timeFilter+` AND startsWith(conversation_id,'claude:') AND `+conversationTraffic+`) WHERE NOT prior_seen AND rn>1 ORDER BY id LIMIT 20`, w.params, &ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range ids {
		out, err := c.read("turns", url.Values{"session_id": {v.ID}, "from": {os.Getenv("ARCHIVE_R6_FROM")}, "to": {os.Getenv("ARCHIVE_R6_TO")}, "limit": {"100"}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		var page struct {
			Total int
			Turns []clickhouseTurn
		}
		json.Unmarshal(b, &page)
		desc := []string{}
		for _, turn := range page.Turns {
			desc = append(desc, fmt.Sprintf("%s/calls=%d/reply=%t", turn.TurnID, turn.Requests, turn.FinalText != ""))
		}
		t.Logf("LIVE_TURNS session=%s total=%d turns=%s", v.ID, page.Total, strings.Join(desc, ","))
	}
}

func TestRound6Regressions(t *testing.T) {
	c := localClickHouse(t)
	c.client.Transport = browserFixtureTransport{}
	for _, file := range []string{"schema.sql", "derived.sql"} {
		b, _ := os.ReadFile("testdata/clickhouse/" + file)
		for _, sql := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
		}
	}
	base := time.Now().UTC().Add(-time.Hour)
	seed := func(id, session, prompt, role string, n int, extra map[string]any) {
		at := base.Add(time.Duration(n) * time.Second).Format("2006-01-02 15:04:05.999999999")
		row := map[string]any{"request_id": id, "conversation_id": session, "started_at": at, "ingested_at": at, "kind": "messages", "outcome": "succeeded", "status_code": 200, "input_items": 200, "system_prompt_hash": 1, "prior_seen": n != 0, "last_user_text": prompt, "reply_text": "real answer", "msg_direction": []string{"input"}, "msg_role": []string{role}, "msg_type": []string{"message"}, "msg_text": []string{prompt}}
		for k, v := range extra {
			row[k] = v
		}
		for _, key := range []string{"msg_position", "msg_tool_name", "msg_call_id", "msg_arguments", "msg_is_error", "msg_text_hash"} {
			n := len(row["msg_role"].([]string))
			switch key {
			case "msg_position", "msg_text_hash":
				row[key] = make([]int, n)
			case "msg_is_error":
				row[key] = make([]bool, n)
			default:
				row[key] = make([]string, n)
			}
		}
		chInsert(t, c, "request_facts", row)
		chInsert(t, c, "requests", map[string]any{"request_id": id, "started_at": at, "completed_at": at, "request_body": `{"input":[{"role":"user","content":"` + prompt + `"}]}`, "response_body": `{"output":[{"role":"assistant","content":"` + id + `"}]}`})
	}
	seed("first", "claude:outoforder", "real prompt", "user", 0, map[string]any{"prior_seen": true, "reply_text": ""})
	seed("later", "claude:outoforder", "tool result", "tool", 1, map[string]any{"prior_seen": false})
	seed("helper", "claude:outoforder", "helper-only-needle", "user", 2, map[string]any{"input_items": 2, "system_prompt_hash": 2, "msg_direction": []string{"input", "input"}, "msg_role": []string{"system", "user"}, "msg_type": []string{"message", "message"}, "msg_text": []string{"helper instructions", "helper-only-needle"}})
	got, err := c.read("turns", url.Values{"session_id": {"claude:outoforder"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	var page struct {
		Total int
		Turns []clickhouseTurn
	}
	json.Unmarshal(b, &page)
	if page.Total != 1 || page.Turns[0].Requests != 3 || page.Turns[0].FinalText != "real answer" {
		t.Fatalf("out of order grouping %s", b)
	}
	stats, err := c.read("insights", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.(Insights).Totals.Turns != 1 {
		t.Fatalf("insights %+v", stats)
	}
	content, _ := collectExport(t, c, url.Values{"id": {"claude:outoforder"}, "format": {"sft"}})
	if !strings.Contains(content, "later") || strings.Contains(content, "helper-only-needle") {
		t.Fatalf("side call SFT %s", content)
	}
	seed("title", "codex:title", "Generate a title that will help the user recognize this T3 Code thread weeks later.", "user", 3, nil)
	seed("compact", "codex:thread:compaction", "summarize", "user", 4, map[string]any{"request_kind": "compaction"})
	seed("normal", "fp:normal", "<current_datetime>now</current_datetime>Actual task", "user", 5, nil)
	for _, show := range []string{"", "1"} {
		got, err := c.read("sessions", url.Values{"show_system": {show}})
		if err != nil {
			t.Fatal(err)
		}
		rows := got.([]archiveSession)
		want := 2
		if show == "1" {
			want = 4
		}
		if len(rows) != want {
			t.Fatalf("system filter show=%s rows=%+v", show, rows)
		}
		for _, row := range rows {
			if row.SessionID == "fp:normal" && row.Summary != "Actual task" {
				t.Fatalf("datetime title %+v", row)
			}
		}
	}
	got, err = c.read("sessions", url.Values{"q": {"helper-only-needle"}})
	if err != nil || len(got.([]archiveSession)) != 0 {
		t.Fatalf("helper search %v %v", got, err)
	}
	for i, v := range []struct{ raw, query string }{
		{"Look &amp; listen", "look & listen"},
		{"A<t3_context>private</t3_context>B", "ab"},
		{"[Assistant quote](t3-citation://thread/first?comment=first+point)", "first point"},
		{"<command-name>/review</command-name><command-args>this diff</command-args>", "/review this diff"},
	} {
		seed(fmt.Sprint("clean-", i), fmt.Sprint("clean-session-", i), v.raw, "user", 10+i, nil)
		got, err := c.read("sessions", url.Values{"q": {v.query}})
		if err != nil || len(got.([]archiveSession)) != 1 {
			t.Fatalf("cleaned search %q matches=%v error=%v", v.query, got, err)
		}
	}

}

func TestRound6SearchHandlerDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c := &clickhouseReader{config: pluginConfig{URL: server.URL}, client: server.Client()}
	p := &Plugin{reader: c}
	raw, _ := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/sessions", Query: url.Values{"q": {"absent"}}})
	start := time.Now()
	data, err := p.HandleContext(context.Background(), "management.handle", raw)
	if err != nil {
		t.Fatal(err)
	}
	var env struct{ Result managementResponse }
	if err = json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.Result.StatusCode != 504 || !strings.Contains(string(env.Result.Body), "search timed out, narrow the range") || time.Since(start) > 4*time.Second {
		t.Fatalf("deadline response status=%d body=%s elapsed=%s", env.Result.StatusCode, env.Result.Body, time.Since(start))
	}
	t.Logf("deadline HTTP=%d body=%s elapsed=%s", env.Result.StatusCode, env.Result.Body, time.Since(start))
}

func TestRound6LiveSFT(t *testing.T) {
	if os.Getenv("ARCHIVE_R6_LIVE") == "" {
		t.Skip("opt-in")
	}
	c := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: "inference_reader", Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{}}
	w, _ := windowFor(url.Values{"from": {os.Getenv("ARCHIVE_R6_FROM")}, "to": {os.Getenv("ARCHIVE_R6_TO")}}, 30)
	var ids []struct {
		ID string `json:"id"`
	}
	if err := c.query(context.Background(), `SELECT DISTINCT conversation_id AS id FROM cliproxy.request_facts WHERE `+timeFilter+` AND startsWith(conversation_id,'claude:18fca967') LIMIT 1`, w.params, &ids); err != nil || len(ids) != 1 {
		t.Fatalf("target session absent %v", err)
	}
	content, pages := collectExport(t, c, url.Values{"id": {ids[0].ID}, "format": {"sft"}, "from": {os.Getenv("ARCHIVE_R6_FROM")}, "to": {os.Getenv("ARCHIVE_R6_TO")}})
	var row struct{ Messages []map[string]any }
	if err := json.Unmarshal([]byte(content), &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Messages) != 2 || row.Messages[0]["content"] != "count" || strings.Contains(content, "Files modified by user:") {
		t.Fatalf("wrong live SFT snapshot messages=%d", len(row.Messages))
	}
	t.Logf("live SFT session=%s messages=%d bytes=%d pages=%d", ids[0].ID, len(row.Messages), len(content), pages)
}
