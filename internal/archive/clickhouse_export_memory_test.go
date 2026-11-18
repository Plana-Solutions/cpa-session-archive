package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This is a resource acceptance check, not a latency benchmark.
func TestClickHouseExportMemory(t *testing.T) {
	c := localClickHouse(t)
	seedBrowserFixture(t, c)
	var settings []struct{ Name, Value string }
	if err := c.query(context.Background(), `SELECT name AS Name,value AS Value FROM system.settings WHERE name IN ('readonly','max_memory_usage','max_result_bytes','max_execution_time') LIMIT 4`, nil, &settings); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range settings {
		got[s.Name] = s.Value
	}
	// query() intentionally tightens execution time to 55 seconds.
	for k, v := range map[string]string{"readonly": "2", "max_memory_usage": "536870912", "max_result_bytes": "8388608", "max_execution_time": "55"} {
		if got[k] != v {
			t.Fatalf("reader setting %s=%s, want %s", k, got[k], v)
		}
	}
	at := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02 15:04:05.999999999")
	chExec(t, c, `INSERT INTO cliproxy.requests (request_id,started_at,completed_at,request_body,response_body) SELECT concat('live-size-',toString(number)),toDateTime64('`+at+`',9,'UTC')+toIntervalSecond(number),toDateTime64('`+at+`',9,'UTC'),concat('{"messages":[{"role":"user","content":"',repeat('x',230000),'"}]}'),'{"text":"answer"}' FROM numbers(200)`)
	chExec(t, c, `INSERT INTO cliproxy.request_facts (request_id,started_at,ingested_at,conversation_id,kind,input_items,tools_offered,msg_direction,msg_position,msg_role,msg_type,msg_text,msg_tool_name,msg_call_id,msg_arguments,msg_is_error,msg_text_hash) SELECT request_id,started_at,started_at,'live-size','messages',200,['a','b'],['input','output'],[0,0],['user','assistant'],['message','message'],[repeat('x',10000),'answer'],['',''],['',''],['',''],[false,false],[0,0] FROM cliproxy.requests WHERE startsWith(request_id,'live-size-')`)
	for _, session := range []string{"mock-session-long-content", "live-size", "external:imported-session"} {
		for _, format := range []string{"sft", "archive"} {
			for _, all := range []bool{false, true} {
				if all && session != "mock-session-long-content" {
					continue
				}
				q := url.Values{"id": {session}, "format": {format}}
				if all {
					q.Set("scope", "all")
					q.Del("id")
				}
				var expected string
				for _, baseline := range []bool{true, false} {
					if baseline && os.Getenv("ARCHIVE_EXPORT_BASELINE") == "" {
						continue
					}
					label := fmt.Sprintf("session=%s format=%s all=%t baseline=%t", session, format, all, baseline)
					started := time.Now().UTC().Format(time.RFC3339Nano)
					if baseline {
						output, err := baselineExport(c, cloneValues(q))
						if err == nil {
							expected = output
						}
						t.Logf("%s bytes=%d error=%v", label, len(output), err)
					} else {
						content, pages := collectExport(t, c, cloneValues(q))
						if len(content) == 0 {
							t.Fatal("empty export")
						}
						if os.Getenv("ARCHIVE_EXPORT_BASELINE") != "" && format == "sft" {
							before := expected
							if before != "" {
								var a, b any
								// Each SFT line is a separate session.
								aa := strings.Split(strings.TrimSpace(before), "\n")
								bb := strings.Split(strings.TrimSpace(content), "\n")
								if len(aa) != len(bb) {
									t.Fatal("SFT session count changed")
								}
								for i := range aa {
									json.Unmarshal([]byte(aa[i]), &a)
									json.Unmarshal([]byte(bb[i]), &b)
									if !reflect.DeepEqual(a, b) {
										t.Fatalf("SFT messages changed session %d", i)
									}
								}
							}
						}
						t.Logf("%s bytes=%d pages=%d", label, len(content), pages)
					}
					chExec(t, c, "SYSTEM FLUSH LOGS")
					var metrics []struct {
						Peak     uint64 `json:"peak"`
						Queries  uint64 `json:"queries"`
						Failures uint64 `json:"failures"`
					}
					if err := c.query(context.Background(), `SELECT max(memory_usage) AS peak,count() AS queries,countIf(type!='QueryFinish') AS failures FROM system.query_log WHERE user='inference_reader' AND query_start_time_microseconds>=parseDateTime64BestEffort({since:String},9,'UTC') AND NOT has(tables,'system.query_log') AND type IN ('QueryFinish','ExceptionWhileProcessing','ExceptionBeforeStart') LIMIT 1`, url.Values{"since": {started}}, &metrics); err != nil {
						t.Fatal(err)
					}
					var perQuery []struct {
						SQL  string `json:"sql"`
						Peak uint64 `json:"peak"`
						N    uint64 `json:"n"`
					}
					if err := c.query(context.Background(), `SELECT any(left(query,160)) AS sql,max(memory_usage) AS peak,count() AS n FROM system.query_log WHERE user='inference_reader' AND query_start_time_microseconds>=parseDateTime64BestEffort({since:String},9,'UTC') AND NOT has(tables,'system.query_log') AND type IN ('QueryFinish','ExceptionWhileProcessing','ExceptionBeforeStart') GROUP BY normalizedQueryHash(query) ORDER BY peak DESC LIMIT 32`, url.Values{"since": {started}}, &perQuery); err != nil {
						t.Fatal(err)
					}
					for _, m := range perQuery {
						t.Logf("QUERY %s peak_bytes=%d count=%d sql=%s", label, m.Peak, m.N, m.SQL)
					}
					m := metrics[0]
					t.Logf("MEMORY %s peak_bytes=%d queries=%d failures=%d", label, m.Peak, m.Queries, m.Failures)
					if !baseline && (m.Peak >= 256<<20 || m.Failures != 0 || m.Queries == 0) {
						t.Fatalf("export exceeded memory target: %+v", m)
					}
				}
			}
		}
	}

}

func baselineExport(c *clickhouseReader, q url.Values) (string, error) {
	var out strings.Builder
	for page := 0; page < 10000; page++ {
		w, e := windowFor(q, 365)
		if e != nil {
			return out.String(), e
		}
		v, e := c.exportBaseline(context.Background(), w, q)
		if e != nil {
			return out.String(), e
		}
		ticket := v.(map[string]any)
		out.WriteString(ticket["content"].(string))
		if ticket["done"].(bool) {
			return out.String(), nil
		}
		q.Set("from", ticket["from"].(string))
		q.Set("to", ticket["to"].(string))
		q.Set("offset", fmt.Sprint(ticket["next_offset"]))
		q.Set("byte_offset", fmt.Sprint(ticket["next_byte_offset"]))
		q.Set("session_offset", fmt.Sprint(ticket["next_session_offset"]))
	}
	return out.String(), fmt.Errorf("cursor did not terminate")
}
