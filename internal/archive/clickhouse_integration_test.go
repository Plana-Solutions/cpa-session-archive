package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localClickHouse(t *testing.T) *clickhouseReader {
	t.Helper()
	binary := os.Getenv("CLICKHOUSE_BINARY")
	if binary == "" {
		binary, _ = exec.LookPath("clickhouse")
	}
	if binary == "" {
		t.Skip("ClickHouse binary missing; set CLICKHOUSE_BINARY")
	}
	port := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	httpPort, tcpPort := port(), port()
	dir := t.TempDir()
	config := fmt.Sprintf(`<clickhouse><listen_host>127.0.0.1</listen_host><http_port>%d</http_port><tcp_port>%d</tcp_port><path>%s/data/</path><tmp_path>%s/tmp/</tmp_path><max_server_memory_usage>1073741824</max_server_memory_usage><max_thread_pool_size>256</max_thread_pool_size><background_pool_size>4</background_pool_size><background_schedule_pool_size>4</background_schedule_pool_size><background_buffer_flush_schedule_pool_size>1</background_buffer_flush_schedule_pool_size><background_message_broker_schedule_pool_size>1</background_message_broker_schedule_pool_size><background_distributed_schedule_pool_size>1</background_distributed_schedule_pool_size><merge_tree><number_of_free_entries_in_pool_to_execute_optimize_entire_partition>2</number_of_free_entries_in_pool_to_execute_optimize_entire_partition><number_of_free_entries_in_pool_to_lower_max_size_of_merge>2</number_of_free_entries_in_pool_to_lower_max_size_of_merge><number_of_free_entries_in_pool_to_execute_mutation>2</number_of_free_entries_in_pool_to_execute_mutation></merge_tree><logger><level>information</level><console>true</console></logger><users><default><password></password><networks><ip>127.0.0.1</ip><ip>::1</ip></networks><profile>default</profile><quota>default</quota></default></users><profiles><default><max_threads>2</max_threads></default></profiles><quotas><default/></quotas></clickhouse>`, httpPort, tcpPort, dir, dir)
	conf := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(conf, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "server", "--config-file="+conf)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		logFile.Close()
	})
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	client := &http.Client{Timeout: 2 * time.Second}
	ready := false
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		r, e := client.Get(endpoint + "/ping")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		b, _ := os.ReadFile(logFile.Name())
		t.Fatalf("ClickHouse not ready: %s", b)
	}
	c := &clickhouseReader{config: pluginConfig{URL: endpoint, Database: "cliproxy", User: "default"}, client: &http.Client{Timeout: 60 * time.Second}}
	return c
}
func chExec(t *testing.T, c *clickhouseReader, sql string) {
	t.Helper()
	r, err := c.client.Post(c.config.URL, "text/plain", strings.NewReader(sql))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 {
		t.Fatalf("ClickHouse fixture: %s", b)
	}
}
func chInsert(t *testing.T, c *clickhouseReader, table string, rows ...map[string]any) {
	t.Helper()
	var body bytes.Buffer
	body.WriteString("INSERT INTO cliproxy." + table + " FORMAT JSONEachRow\n")
	for _, row := range rows {
		b, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		body.Write(b)
		body.WriteByte('\n')
	}
	chExec(t, c, body.String())
}
func TestClickHouseHandlers(t *testing.T) {
	c := localClickHouse(t)
	for _, file := range []string{"schema.sql", "derived.sql"} {
		b, e := os.ReadFile("testdata/clickhouse/" + file)
		if e != nil {
			t.Fatal(e)
		}
		for _, stmt := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(stmt, "{db}", "cliproxy"))
		}
	}
	ts := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05.999999999")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprint("req-", i)
		conversation := "codex:thread"
		model := "gpt-6.1-sol"
		outcome := "succeeded"
		if i == 3 {
			conversation = ""
			model = "claude-opus"
			outcome = "failed"
		}
		chInsert(t, c, "requests", map[string]any{"request_id": id, "trace_id": id, "started_at": ts, "completed_at": ts, "model": model, "request_body": `{"instructions":"test instructions","input":[{"role":"user","content":"hello"}]}`, "response_body": `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"reply"}]}]}`})
		chInsert(t, c, "request_facts", map[string]any{"request_id": id, "started_at": ts, "ingested_at": ts, "model": model, "conversation_id": conversation, "client": "codex", "turn_id": "turn-1", "outcome": outcome, "status_code": 200, "duration_ms": 2500, "last_user_text": "hello '+ DROP TABLE requests; --", "reply_text": "reply", "reasoning_text": "thinking", "tools_called": []string{"shell"}, "msg_direction": []string{"input", "output", "output"}, "msg_position": []int{0, 0, 1}, "msg_role": []string{"user", "assistant", "tool"}, "msg_type": []string{"message", "function_call", "function_call_output"}, "msg_tool_name": []string{"", "shell", "shell"}, "msg_call_id": []string{"", "call-1", "call-1"}, "msg_arguments": []string{"", "{}", ""}, "msg_is_error": []bool{false, false, false}, "msg_text": []string{"hello", "", "result"}, "msg_text_hash": []uint64{1, 0, 2}})
		for attempt := 0; attempt < 2; attempt++ {
			chInsert(t, c, "usage", map[string]any{"request_id": fmt.Sprintf("%s-attempt-%d", id, attempt), "trace_id": id, "requested_at": ts, "api_key_hash": "1234567890abcdef", "input_tokens": 10, "output_tokens": 2, "reasoning_tokens": 1, "total_tokens": 12})
		}
	}
	p := &Plugin{reader: c}
	call := func(endpoint string, params url.Values, status int) json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/" + endpoint, Query: params})
		reply, e := p.handleManagement(raw)
		if e != nil {
			t.Fatal(e)
		}
		var env struct{ Result managementResponse }
		if e = json.Unmarshal(reply, &env); e != nil {
			t.Fatal(e)
		}
		if env.Result.StatusCode != status {
			t.Fatalf("%s status %d: %s", endpoint, env.Result.StatusCode, env.Result.Body)
		}
		return env.Result.Body
	}
	t.Run("all endpoints without native table", func(t *testing.T) {
		var stats Stats
		json.Unmarshal(call("stats", nil, 200), &stats)
		if stats.Records != 3 || stats.Sessions != 1 {
			t.Fatalf("stats %+v", stats)
		}
		var rows []archiveSession
		json.Unmarshal(call("sessions", nil, 200), &rows)
		if len(rows) != 1 || rows[0].Requests != 2 || rows[0].TotalTokens != 48 {
			t.Fatalf("sessions %+v", rows)
		}
		for _, params := range []url.Values{{"client": {"codex"}}, {"key.id": {"1234567890abcdef"}}, {"model.resolved": {"gpt-6.1-sol"}}, {"q": {"'+ DROP TABLE requests; --"}}} {
			json.Unmarshal(call("sessions", params, 200), &rows)
			if len(rows) != 1 {
				t.Fatalf("filter %v", params)
			}
		}
		json.Unmarshal(call("sessions", url.Values{"client": {"not-present"}}, 200), &rows)
		if len(rows) != 0 {
			t.Fatal("filter ignored")
		}
		call("facets", nil, 200)
		call("export", url.Values{"id": {"codex:thread"}, "format": {"archive"}}, 200)
		var insights Insights
		json.Unmarshal(call("insights", url.Values{"days": {"7"}}, 200), &insights)
		if insights.Totals.Requests != 3 || insights.Totals.Sessions != 1 || len(insights.Daily) != 7 {
			t.Fatalf("insights %+v", insights)
		}
		call("insights", url.Values{"from": {time.Now().UTC().Format(time.DateOnly)}, "to": {time.Now().UTC().Format(time.DateOnly)}}, 200)
		for _, endpoint := range []string{"turns", "turn-text"} {
			params := url.Values{"session_id": {"codex:thread"}, "turn_id": {"req-1"}}
			call(endpoint, params, 200)
		}
		call("turns", url.Values{"session_id": {"codex:thread"}, "order": {"desc"}}, 200)
		for _, endpoint := range []string{"requests", "request-view", "request-context"} {
			call(endpoint, url.Values{"id": {"req-1"}}, 200)
			call(endpoint, url.Values{"id": {"absent"}}, 404)
		}
		var detail Record
		if err := json.Unmarshal(call("requests", url.Values{"id": {"req-1"}}, 200), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.SessionID != "codex:thread" || !strings.Contains(string(detail.OriginalRequest), "test instructions") || !strings.Contains(string(detail.Response), "reply") {
			t.Fatal("raw request mapping lost content")
		}
		var view RequestTimelineView
		if err := json.Unmarshal(call("request-view", url.Values{"id": {"req-1"}}, 200), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Entries) != 3 || view.Entries[1].Role != "tool_call" || view.Entries[2].Role != "tool_result" {
			t.Fatalf("timeline %+v", view.Entries)
		}
		var page struct {
			Turns []clickhouseTurn `json:"turns"`
			Total int              `json:"total"`
		}
		json.Unmarshal(call("turns", url.Values{"session_id": {"codex:thread"}, "limit": {"1"}, "offset": {"1"}}, 200), &page)
		if page.Total != 2 || len(page.Turns) != 1 || page.Turns[0].TurnID != "req-2" || page.Turns[0].TotalTokens != 24 {
			t.Fatalf("turn pagination %+v", page)
		}
		sum := int64(0)
		for _, day := range insights.Daily {
			for _, n := range day.Requests {
				sum += n
			}
		}
		if sum != insights.Totals.Requests {
			t.Fatal("daily counts do not reconcile")
		}

		call("turns", nil, 400)
		call("insights", url.Values{"from": {"bad-date"}}, 400)
		call("sessions", url.Values{"limit": {"1001"}}, 400)
		call("sessions", url.Values{"q": {"x' OR 1=1 --"}}, 200)
		call("external-sources", nil, 200)
	})
	b, e := os.ReadFile("testdata/clickhouse/external.sql")
	if e != nil {
		t.Fatal(e)
	}
	chExec(t, c, string(b))
	t.Run("empty imported table", func(t *testing.T) { call("sessions", nil, 200); call("facets", nil, 200); call("stats", nil, 200) })
	chInsert(t, c, "external_messages", map[string]any{"session_id": "native-1", "native_session_id": "original-1", "source": "claude", "project": "repo", "title": "Imported transcript", "ts": ts, "position": 1, "role": "user", "type": "message", "text": "Native prompt"}, map[string]any{"session_id": "native-1", "source": "claude", "project": "repo", "title": "Imported transcript", "ts": ts, "position": 2, "role": "assistant", "type": "message", "text": "Native reply"})
	t.Run("imported sessions", func(t *testing.T) {
		var rows []archiveSession
		json.Unmarshal(call("sessions", url.Values{"client": {"claude"}}, 200), &rows)
		if len(rows) != 1 || rows[0].Requests != 2 || rows[0].SourceKind != "external-local-history" {
			t.Fatalf("imported %+v", rows)
		}
		call("facets", nil, 200)
		call("stats", nil, 200)
		call("turns", url.Values{"session_id": {"external:native-1"}}, 200)
		call("export", url.Values{"id": {"external:native-1"}, "format": {"archive"}}, 200)
		call("turns", url.Values{"session_id": {"external:native-1"}, "turn_id": {"1"}, "offset": {"1"}}, 200)
		for _, endpoint := range []string{"turns", "turn-text"} {
			call(endpoint, url.Values{"session_id": {"external:native-1"}, "turn_id": {"1"}}, 200)
		}
		for _, endpoint := range []string{"requests", "request-view", "request-context"} {
			call(endpoint, url.Values{"id": {"external:native-1:1"}}, 200)
		}
	})
	var present []struct {
		N uint64 `json:"n"`
	}
	if e := c.query(context.Background(), "SELECT count() AS n FROM cliproxy.requests", nil, &present); e != nil || present[0].N != 3 {
		t.Fatal("parameterization damaged fixtures", e)
	}
}
