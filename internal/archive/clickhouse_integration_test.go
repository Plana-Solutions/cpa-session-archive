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
	config := fmt.Sprintf(`<clickhouse><listen_host>127.0.0.1</listen_host><http_port>%d</http_port><tcp_port>%d</tcp_port><path>%s/data/</path><tmp_path>%s/tmp/</tmp_path><max_server_memory_usage>1610612736</max_server_memory_usage><max_thread_pool_size>256</max_thread_pool_size><background_pool_size>4</background_pool_size><background_schedule_pool_size>4</background_schedule_pool_size><background_buffer_flush_schedule_pool_size>1</background_buffer_flush_schedule_pool_size><background_message_broker_schedule_pool_size>1</background_message_broker_schedule_pool_size><background_distributed_schedule_pool_size>1</background_distributed_schedule_pool_size><merge_tree><number_of_free_entries_in_pool_to_execute_optimize_entire_partition>2</number_of_free_entries_in_pool_to_execute_optimize_entire_partition><number_of_free_entries_in_pool_to_lower_max_size_of_merge>2</number_of_free_entries_in_pool_to_lower_max_size_of_merge><number_of_free_entries_in_pool_to_execute_mutation>2</number_of_free_entries_in_pool_to_execute_mutation></merge_tree><query_log><database>system</database><table>query_log</table><flush_interval_milliseconds>100</flush_interval_milliseconds></query_log><logger><level>information</level><console>true</console></logger><users><default><password></password><networks><ip>127.0.0.1</ip><ip>::1</ip></networks><profile>default</profile><quota>default</quota></default></users><profiles><default><max_threads>2</max_threads></default></profiles><quotas><default/></quotas></clickhouse>`, httpPort, tcpPort, dir, dir)
	if os.Getenv("ARCHIVE_TURN_SEARCH_ACCEPTANCE") != "" || os.Getenv("ARCHIVE_TURN_SEARCH_SYNTHETIC") != "" {
		start, end := strings.Index(config, "<query_log>"), strings.Index(config, "</query_log>")+len("</query_log>")
		config = config[:start] + config[end:]
		config = strings.Replace(config, "<level>information</level>", "<level>warning</level>", 1)
		config = strings.Replace(config, "<merge_tree>", "<merge_tree><merge_max_block_size>256</merge_max_block_size><max_bytes_to_merge_at_max_space_in_pool>2147483648</max_bytes_to_merge_at_max_space_in_pool>", 1)
	}
	if err := os.Mkdir(filepath.Join(dir, "users.d"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := `<clickhouse><profiles><inference_reader><readonly>2</readonly><max_memory_usage>536870912</max_memory_usage><max_execution_time>60</max_execution_time><max_result_bytes>8388608</max_result_bytes><result_overflow_mode>throw</result_overflow_mode><log_queries>1</log_queries></inference_reader></profiles><users><inference_reader><password></password><networks><ip>127.0.0.1</ip><ip>::1</ip></networks><profile>inference_reader</profile><quota>default</quota></inference_reader></users></clickhouse>`
	if os.Getenv("ARCHIVE_TURN_SEARCH_ACCEPTANCE") != "" || os.Getenv("ARCHIVE_TURN_SEARCH_SYNTHETIC") != "" {
		profile = strings.Replace(profile, "<log_queries>1</log_queries>", "<log_queries>0</log_queries>", 1)
	}
	if err := os.WriteFile(filepath.Join(dir, "users.d", "inference-reader.xml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	usersStart := strings.Index(config, "<users>")
	usersEnd := strings.Index(config, "</quotas>") + len("</quotas>")
	users := config[usersStart:usersEnd]
	if err := os.WriteFile(filepath.Join(dir, "users.xml"), []byte("<clickhouse>"+users+"</clickhouse>"), 0600); err != nil {
		t.Fatal(err)
	}
	config = config[:usersStart] + "<users_config>" + filepath.Join(dir, "users.xml") + "</users_config>" + config[usersEnd:]
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
	cmd.Env = append(os.Environ(), "CLICKHOUSE_WATCHDOG_ENABLE=0")
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
	c := &clickhouseReader{config: pluginConfig{URL: endpoint, Database: "cliproxy", User: "inference_reader"}, client: &http.Client{Timeout: 60 * time.Second}}
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
		chInsert(t, c, "request_facts", map[string]any{"request_id": id, "started_at": ts, "ingested_at": ts, "model": model, "conversation_id": conversation, "client": "codex", "turn_id": "turn-1", "outcome": outcome, "status_code": 200, "duration_ms": 2500, "last_user_text": "hello '+ DROP TABLE requests; --", "reply_text": "reply", "reasoning_text": "thinking", "tools_called": []string{"shell"}, "msg_direction": []string{"input", "output", "output"}, "msg_position": []int{0, 0, 1}, "msg_role": []string{"user", "assistant", "tool"}, "msg_type": []string{"message", "function_call", "function_call_output"}, "msg_tool_name": []string{"", "shell", "shell"}, "msg_call_id": []string{"", "call-1", "call-1"}, "msg_arguments": []string{"", "{}", ""}, "msg_is_error": []bool{false, false, false}, "msg_text": []string{"hello '+ DROP TABLE requests; --", "", "result"}, "msg_text_hash": []uint64{1, 0, 2}})
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
			params := url.Values{"session_id": {"codex:thread"}, "turn_id": {"turn-1"}}
			call(endpoint, params, 200)
		}
		call("turns", url.Values{"session_id": {"codex:thread"}, "order": {"desc"}}, 200)
		for _, endpoint := range []string{"requests", "request-view", "request-context"} {
			call(endpoint, url.Values{"id": {"req-1"}, "call_id": {"call-1"}}, 200)
			call(endpoint, url.Values{"id": {"absent"}}, 404)
		}
		var detail Record
		if err := json.Unmarshal(call("requests", url.Values{"id": {"req-1"}, "call_id": {"call-1"}}, 200), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.SessionID != "codex:thread" || !strings.Contains(string(detail.OriginalRequest), "test instructions") || !strings.Contains(string(detail.Response), "reply") {
			t.Fatal("raw request mapping lost content")
		}
		var view RequestTimelineView
		if err := json.Unmarshal(call("request-view", url.Values{"id": {"req-1"}, "call_id": {"call-1"}}, 200), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Entries) != 3 || view.Entries[1].Role != "tool_call" || view.Entries[2].Role != "tool_result" {
			t.Fatalf("timeline %+v", view.Entries)
		}
		var page struct {
			Turns []clickhouseTurn `json:"turns"`
			Total int              `json:"total"`
		}
		json.Unmarshal(call("turns", url.Values{"session_id": {"codex:thread"}, "limit": {"1"}, "offset": {"0"}}, 200), &page)
		if page.Total != 1 || len(page.Turns) != 1 || page.Turns[0].TurnID != "turn-1" || page.Turns[0].TotalTokens != 48 {
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
		for _, literal := range []string{"%", "_", `\`} {
			json.Unmarshal(call("sessions", url.Values{"q": {literal}}, 200), &rows)
			if len(rows) != 0 {
				t.Fatalf("search treated %q as a wildcard", literal)
			}
		}
	})
	t.Run("injected context titles and turn starts", func(t *testing.T) {
		for _, fixture := range []struct {
			id        string
			texts     []string
			want      string
			continued bool
		}{
			{"claude:preamble", []string{"<system-reminder>Codebase and user instructions are shown below. IMPORTANT: obey.</system-reminder>", "<command-name>/poteto-mode</command-name>", "Base directory for this skill: /plugins/poteto-mode", "Fix the Claude archive"}, "Fix the Claude archive", false},
			{"codex:preamble", []string{"# AGENTS.md instructions for /repo\n<INSTRUCTIONS>Use tests</INSTRUCTIONS>", "<environment_context><cwd>/repo</cwd></environment_context>", "Fix the Codex archive"}, "Fix the Codex archive", false},
			{"claude:continued", []string{"This session is being continued from a previous conversation that ran out of context. Summary: old details", "<system-reminder>Read tool result</system-reminder>", "Resume the archive tests"}, "Resume the archive tests", true},
		} {
			dirs, roles, types := []string{}, []string{}, []string{}
			for range fixture.texts {
				dirs = append(dirs, "input")
				roles = append(roles, "user")
				types = append(types, "message")
			}
			chInsert(t, c, "request_facts", map[string]any{"request_id": fixture.id, "conversation_id": fixture.id, "started_at": ts, "kind": "messages", "last_user_text": "injected last-user preamble", "msg_direction": dirs, "msg_role": roles, "msg_type": types, "msg_text": fixture.texts})
			value, err := c.read("sessions", url.Values{})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range value.([]archiveSession) {
				if row.SessionID == fixture.id {
					found = true
					if row.Summary != fixture.want || row.Continued != fixture.continued {
						t.Fatalf("session %+v", row)
					}
				}
			}
			if !found {
				t.Fatal("missing fixture session")
			}
			value, err = c.read("turns", url.Values{"session_id": {fixture.id}})
			if err != nil {
				t.Fatal(err)
			}
			turns := value.(map[string]any)["turns"].([]clickhouseTurn)
			if len(turns) != 1 || turns[0].UserText != fixture.want || turns[0].Continued != fixture.continued {
				t.Fatalf("turns %+v", turns)
			}
		}
	})

	t.Run("full genuine turn text", func(t *testing.T) {
		full := "Review " + strings.Repeat("long prompt ", 6000) + "end"
		chInsert(t, c, "request_facts", map[string]any{"request_id": "long-text", "conversation_id": "claude:long-text", "started_at": ts, "kind": "messages", "last_user_text": "injected preamble", "msg_direction": []string{"input", "input"}, "msg_role": []string{"user", "user"}, "msg_type": []string{"message", "message"}, "msg_text": []string{"<system-reminder>injected context</system-reminder>", full}})
		value, err := c.read("turn-text", url.Values{"session_id": {"claude:long-text"}, "turn_id": {"long-text"}})
		if err != nil {
			t.Fatal(err)
		}
		if value.(map[string]string)["text"] != full {
			t.Fatal("full text was truncated or retained injected context")
		}
	})

	b, e := os.ReadFile("testdata/clickhouse/external.sql")
	if e != nil {
		t.Fatal(e)
	}
	chExec(t, c, string(b))
	c.externalChecked = time.Time{}
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
	t.Run("Claude turns, large exports and bounded request view", func(t *testing.T) {
		base := time.Now().UTC().Add(-time.Hour)
		for i := 0; i < 5; i++ {
			at := base.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05.999999999")
			roles := []string{"user", "user", "assistant"}
			types := []string{"message", "message", "message"}
			texts := []string{"/skill", "SKILL BODY", "partial reply"}
			dirs := []string{"input", "input", "output"}
			kind := "messages"
			reply := "partial reply"
			if i == 1 || i == 2 {
				roles[0] = "tool"
				types[0] = "tool_result"
				texts[0] = "tool result"
				texts[1] = "Tool loaded."
				reply = "final reply"
				texts[2] = reply
			}
			if i == 3 {
				kind = "count_tokens"
				reply = "ignored"
			}
			if i == 4 {
				texts[0] = "next question"
				reply = "next answer"
				texts[2] = reply
			}
			chInsert(t, c, "requests", map[string]any{"request_id": fmt.Sprintf("claude-%d", i), "started_at": at, "completed_at": at, "requested_model": "requested-alias", "request_body": `{"messages":[],"padding":"` + strings.Repeat("x", 7<<20) + `"}`, "response_body": `{"text":"complete"}`})
			chInsert(t, c, "request_facts", map[string]any{"request_id": fmt.Sprintf("claude-%d", i), "started_at": at, "ingested_at": at, "conversation_id": "claude:fixture", "model": "claude-opus", "client": "claude", "kind": kind, "prior_seen": i > 0, "input_items": 200, "tools_offered": []string{"a", "b"}, "last_user_text": "SKILL BODY", "reply_text": reply, "outcome": "succeeded", "status_code": 200, "request_kind": "turn", "thread_source": "test", "msg_direction": dirs, "msg_position": []int{0, 1, 0}, "msg_role": roles, "msg_type": types, "msg_text": texts, "msg_tool_name": []string{"", "", ""}, "msg_call_id": []string{"call-x", "", ""}, "msg_arguments": []string{"", "", ""}, "msg_is_error": []bool{false, false, false}, "msg_text_hash": []int{0, 0, 0}})
		}
		value, err := c.read("request-context", url.Values{"id": {"claude-1"}, "call_id": {"call-x"}})
		if err != nil {
			t.Fatal(err)
		}
		recs := value.([]Record)
		var contextOutput struct {
			Output []map[string]string `json:"output"`
		}
		json.Unmarshal(recs[0].Response, &contextOutput)
		if len(contextOutput.Output) != 1 || contextOutput.Output[0]["call_id"] != "call-x" {
			t.Fatal("context must exclude the current request and match the call", contextOutput)
		}
		params := url.Values{"session_id": {"claude:fixture"}}
		value, err = c.read("turns", params)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(value)
		var page struct {
			Turns []clickhouseTurn
			Total int
		}
		json.Unmarshal(b, &page)
		if page.Total != 2 || page.Turns[0].Requests != 3 || page.Turns[0].UserText != "/skill" || page.Turns[0].FinalText != "final reply" {
			t.Fatalf("grouping %+v", page)
		}
		params.Set("turn_id", "claude-0")
		params.Set("limit", "1")
		params.Set("offset", "1")
		value, err = c.read("turns", params)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(value)
		var detail struct {
			Records []RequestTimelineView
			Total   int
		}
		json.Unmarshal(b, &detail)
		if detail.Total != 3 || len(detail.Records) != 1 || detail.Records[0].RequestID != "claude-1" {
			t.Fatalf("steps %s", b)
		}
		value, err = c.read("turn-text", params)
		if err != nil || value.(map[string]string)["text"] != "/skill" {
			t.Fatal("turn text", value, err)
		}
		for _, format := range []string{"archive", "sft"} {
			content, pages := collectExport(t, c, url.Values{"id": {"claude:fixture"}, "format": {format}})
			lines := strings.Split(strings.TrimSpace(content), "\n")
			if format == "archive" && (len(content) < 18<<20 || len(lines) != 2 || pages < 15) {
				t.Fatalf("large archive bytes=%d lines=%d pages=%d", len(content), len(lines), pages)
			}
			if format == "sft" && (!strings.Contains(content, "next question") || strings.Contains(content, "ignored")) {
				t.Fatal("invalid SFT content")
			}
		}
		rows, err := c.read("sessions", url.Values{"model.requested": {"requested-alias"}})
		if err != nil || len(rows.([]archiveSession)) != 1 {
			t.Fatal("requested model", rows, err)
		}
		facets, err := c.read("facets", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(facets)
		for _, name := range []string{"tool.name", "request.kind", "thread.source", "model.requested"} {
			if !strings.Contains(string(b), name) {
				t.Fatal("missing facet", name)
			}
		}
		n := 240
		dirs, roles, types, texts, tools, calls, args := []string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
		positions := []int{}
		flags := []bool{}
		hashes := []int{}
		for i := 0; i < n; i++ {
			d, r := "input", "user"
			if i >= n/2 {
				d, r = "output", "assistant"
			}
			dirs = append(dirs, d)
			roles = append(roles, r)
			types = append(types, "message")
			texts = append(texts, strings.Repeat("\x01", 20000))
			tools = append(tools, "")
			calls = append(calls, "")
			args = append(args, strings.Repeat("\x02", 20000))
			positions = append(positions, i)
			flags = append(flags, false)
			hashes = append(hashes, 0)
		}
		at := base.Format("2006-01-02 15:04:05.999999999")
		chInsert(t, c, "request_facts", map[string]any{"request_id": "large-view", "conversation_id": "large:view", "started_at": at, "ingested_at": at, "msg_direction": dirs, "msg_position": positions, "msg_role": roles, "msg_type": types, "msg_text": texts, "msg_tool_name": tools, "msg_call_id": calls, "msg_arguments": args, "msg_is_error": flags, "msg_text_hash": hashes})
		value, err = c.read("request-view", url.Values{"id": {"large-view"}})
		if err != nil {
			t.Fatal(err)
		}
		view := value.(RequestTimelineView)
		b, _ = json.Marshal(view)
		if len(b) >= resultLimit || len(view.Entries) != 160 || view.Entries[159].Role != "assistant" {
			t.Fatalf("view bytes=%d entries=%d", len(b), len(view.Entries))
		}
		content, _ := collectExport(t, c, url.Values{"id": {"external:native-1"}, "format": {"sft"}})
		if !strings.Contains(content, "Native reply") {
			t.Fatal("native SFT")
		}
	})

	t.Run("side calls remain steps and cannot supply replies or search hits", func(t *testing.T) {
		before, err := c.read("insights", nil)
		if err != nil {
			t.Fatal(err)
		}
		base := time.Now().UTC().Add(-30 * time.Minute)
		for i := 0; i < 6; i++ {
			prompt, reply := "real prompt", "real partial"
			prior := i > 0
			items := 200
			tools := []string{"a", "b"}
			role := "user"
			if i%2 == 1 {
				prompt = "side-call-only-needle"
				reply = "wrong side answer"
				items = 2
				tools = []string{"WebSearch"}
			}
			if i == 2 {
				role = "tool"
				prompt = "tool result"
				reply = "real final"
			}
			if i == 4 {
				prompt = "next real prompt"
				reply = "next real answer"
			}
			at := base.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05.999999999")
			dirs, roles, types, texts := []string{"input"}, []string{role}, []string{"message"}, []string{prompt}
			hash := 1
			if i%2 == 1 {
				hash = 2
				dirs = []string{"input", "input"}
				roles = []string{"system", "user"}
				types = []string{"message", "message"}
				texts = []string{"side instructions", prompt}
			}
			chInsert(t, c, "request_facts", map[string]any{"request_id": fmt.Sprintf("side-%d", i), "conversation_id": "claude:side-fixture", "started_at": at, "ingested_at": at, "model": "claude-side-fixture", "kind": "messages", "prior_seen": prior, "input_items": items, "tools_offered": tools, "last_user_text": prompt, "reply_text": reply, "system_prompt_hash": hash, "msg_direction": dirs, "msg_role": roles, "msg_type": types, "msg_text": texts})
		}
		q := url.Values{"session_id": {"claude:side-fixture"}}
		result, err := c.read("turns", q)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Turns []clickhouseTurn
			Total int
		}
		b, _ := json.Marshal(result)
		json.Unmarshal(b, &page)
		if page.Total != 2 || page.Turns[0].UserText != "real prompt" || page.Turns[0].FinalText != "real final" || page.Turns[0].Requests != 4 || page.Turns[1].FinalText != "next real answer" || page.Turns[1].Requests != 2 {
			t.Fatalf("side call grouping %s", b)
		}
		q.Set("turn_id", "side-0")
		result, err = c.read("turns", q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(result)
		var detail struct{ Records []RequestTimelineView }
		json.Unmarshal(b, &detail)
		if len(detail.Records) != 4 || detail.Records[1].RequestID != "side-1" {
			t.Fatalf("side step missing %s", b)
		}
		result, err = c.read("sessions", url.Values{"q": {"side-call-only-needle"}})
		if err != nil || len(result.([]archiveSession)) != 0 {
			t.Fatal("side call searchable", result, err)
		}
		after, err := c.read("insights", nil)
		if err != nil {
			t.Fatal(err)
		}
		if after.(Insights).Totals.Turns-before.(Insights).Totals.Turns != 2 {
			t.Fatal("Insights side call turns", before, after)
		}
	})
	t.Run("export keeps its first time bound while new sessions arrive", func(t *testing.T) {
		q := url.Values{"scope": {"all"}, "format": {"archive"}}
		result, err := c.read("export", q)
		if err != nil {
			t.Fatal(err)
		}
		ticket := result.(map[string]any)
		cutoff := ticket["to"].(string)
		at, err := time.Parse(time.RFC3339Nano, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		chInsert(t, c, "external_messages", map[string]any{"session_id": "000-new-after-export", "source": "test", "position": 1, "role": "user", "text": "must not enter pinned export", "ts": at.Add(time.Second).Format("2006-01-02 15:04:05.999999999")})
		q.Set("to", cutoff)
		q.Set("from", ticket["from"].(string))
		content, _ := collectExport(t, c, q)
		if strings.Contains(content, "must not enter pinned export") {
			t.Fatal("export bound drifted")
		}
	})

	var present []struct {
		N uint64 `json:"n"`
	}
	if e := c.query(context.Background(), "SELECT count() AS n FROM cliproxy.requests", nil, &present); e != nil || present[0].N != 8 {
		t.Fatal("parameterization damaged fixtures", e)
	}
}

func collectExport(t *testing.T, c *clickhouseReader, q url.Values) (string, int) {
	t.Helper()
	var content strings.Builder
	for pages := 1; pages < 10000; pages++ {
		p := &Plugin{reader: c}
		raw, _ := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/export", Query: q})
		response, err := p.Handle("management.handle", raw)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct{ Result managementResponse }
		json.Unmarshal(response, &envelope)
		if envelope.Result.StatusCode != 200 {
			t.Fatalf("export HTTP %d: %s", envelope.Result.StatusCode, envelope.Result.Body)
		}
		registration, _ := p.Handle("plugin.register", []byte(`{}`))
		var registered struct {
			Result struct {
				Schema int `json:"schema_version"`
			}
		}
		json.Unmarshal(registration, &registered)
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(cpaHostManagementBody(t, registered.Result.Schema, envelope.Result.Body)))
		decoder.UseNumber()
		err = decoder.Decode(&value)
		if err != nil {
			t.Fatal(err)
		}
		ticket := value
		content.WriteString(ticket["content"].(string))
		if ticket["done"].(bool) {
			for _, line := range strings.Split(strings.TrimSpace(content.String()), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !json.Valid([]byte(line)) {
					t.Fatal("invalid JSONL export")
				}
			}
			return content.String(), pages
		}
		q.Set("to", ticket["to"].(string))
		q.Set("from", ticket["from"].(string))
		q.Set("offset", fmt.Sprint(ticket["next_offset"]))
		q.Set("byte_offset", fmt.Sprint(ticket["next_byte_offset"]))
		q.Set("session_offset", fmt.Sprint(ticket["next_session_offset"]))
	}
	t.Fatal("export cursor did not terminate")
	return "", 0
}

func TestClickHouseLookupIndexes(t *testing.T) {
	c := localClickHouse(t)
	for _, file := range []string{"schema.sql", "derived.sql"} {
		b, err := os.ReadFile("testdata/clickhouse/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(stmt, "{db}", "cliproxy"))
		}
	}
	chExec(t, c, `INSERT INTO cliproxy.request_facts (request_id,conversation_id,started_at,ingested_at) SELECT concat('req-',toString(number)),concat('session-',toString(intDiv(number,12))),toDateTime64('2026-11-18',9,'UTC')+toIntervalSecond(number),toDateTime64('2026-11-18',9,'UTC') FROM numbers(300000)`)
	s := &summaryTransport{base: http.DefaultTransport}
	c.client.Transport = s
	measure := func(label string) {
		for _, filter := range []string{`conversation_id='session-24990'`, `request_id='req-299842'`} {
			s.rows = 0
			s.bytes = 0
			var rows []struct {
				N uint64 `json:"n"`
			}
			err := c.query(context.Background(), `SELECT count() AS n FROM cliproxy.request_facts WHERE started_at>='2026-11-18' AND started_at<'2026-11-22' AND `+filter+` LIMIT 1 SETTINGS use_query_condition_cache=0`, nil, &rows)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].N == 0 {
				t.Fatal("lookup lost rows")
			}
			t.Logf("%s %s rows=%d bytes=%d matches=%d", label, filter, s.rows, s.bytes, rows[0].N)
			if label == "indexed" && s.rows >= 300000 {
				t.Fatal("lookup index did not prune")
			}
		}
	}
	measure("baseline")
	migration, err := os.ReadFile("../../migrations/001-request-facts-search.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(migration), ";") {
		if strings.TrimSpace(stmt) != "" {
			chExec(t, c, stmt)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		var rows []struct {
			N uint64 `json:"n"`
		}
		if err = c.query(context.Background(), `SELECT count() AS n FROM system.mutations WHERE database='cliproxy' AND NOT is_done LIMIT 1`, nil, &rows); err != nil {
			t.Fatal(err)
		}
		if rows[0].N == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index materialization incomplete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	measure("indexed")
}
