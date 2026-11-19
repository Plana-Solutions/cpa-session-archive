package archive

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type turnSearchMeasureTransport struct{ summaryTransport }

func (s *turnSearchMeasureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	q := r.URL.Query()
	q.Set("max_threads", "1")
	q.Set("use_query_cache", "0")
	q.Set("use_query_condition_cache", "0")
	r.URL.RawQuery = q.Encode()
	resp, err := s.summaryTransport.RoundTrip(r)
	if err == nil && resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(string(b)))
		fmt.Fprintf(os.Stderr, "LOCAL_EXCEPTION %s\n", b)
	}
	return resp, err
}
func turnSearchCopyLive(t *testing.T, c *clickhouseReader, to time.Time) {
	t.Helper()

	if path := os.Getenv("ARCHIVE_TURN_SEARCH_COPY"); path != "" {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		d := json.NewDecoder(gz)
		d.UseNumber()
		batch := []map[string]any{}
		count := 0
		for {
			var row map[string]any
			err := d.Decode(&row)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			batch = append(batch, row)
			count++
			if len(batch) == 64 {
				chInsert(t, c, "request_facts", batch...)
				batch = nil
			}
		}
		if len(batch) > 0 {
			chInsert(t, c, "request_facts", batch...)
		}
		t.Logf("COPY restored_rows=%d", count)
		return
	}
	live := &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: os.Getenv("CLICKHOUSE_USER"), Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{Timeout: 60 * time.Second}}
	if live.config.URL == "" || live.config.User == "" {
		t.Fatal("reader credentials required")
	}
	// Copy derived fields only. No requests, bodies, arguments or raw response text.
	columns := `request_id,started_at,ingested_at,conversation_id,client,model,kind,request_kind,thread_source,turn_id,prior_seen,input_items,previous_response_id,toString(system_prompt_hash) AS system_prompt_hash,tools_offered,tools_called,outcome,msg_direction,msg_role,msg_type,msg_text`
	p := url.Values{"to": {to.Format("2006-01-02 15:04:05.999999999")}, "from": {to.AddDate(0, 0, -30).Format("2006-01-02 15:04:05.999999999")}}
	var bounds []struct {
		First time.Time `json:"first"`
		Last  time.Time `json:"last"`
		N     uint64    `json:"n"`
	}
	if err := live.query(context.Background(), `SELECT min(started_at) AS first,max(started_at) AS last,count() AS n FROM cliproxy.request_facts WHERE `+timeFilter, p, &bounds); err != nil {
		t.Fatal(err)
	}
	if len(bounds) != 1 || bounds[0].N == 0 {
		t.Fatal("no live rows")
	}
	path := filepath.Join(os.Getenv("ARCHIVE_TURN_SEARCH_EVIDENCE"), "local-derived.jsonl.gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	encoder := json.NewEncoder(gz)
	total := 0
	for lo := bounds[0].First.Truncate(15 * time.Minute); lo.Before(to); lo = lo.Add(15 * time.Minute) {
		hi := lo.Add(15 * time.Minute)
		if hi.After(to) {
			hi = to
		}
		p.Set("from", lo.Format("2006-01-02 15:04:05.999999999"))
		p.Set("to", hi.Format("2006-01-02 15:04:05.999999999"))
		seek := ""
		for {
			round7DiskGuard(t)
			var rows []map[string]any
			if err := live.query(context.Background(), `SELECT `+columns+` FROM cliproxy.request_facts WHERE `+timeFilter+seek+` ORDER BY started_at,request_id LIMIT 64 SETTINGS max_threads=1,max_block_size=8,max_memory_usage=268435456`, p, &rows); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				for _, key := range []string{"started_at", "ingested_at"} {
					v, err := time.Parse(time.RFC3339Nano, row[key].(string))
					if err != nil {
						t.Fatal(err)
					}
					row[key] = v.UTC().Format("2006-01-02 15:04:05.999999999")
				}
				// Prompt hashes arrive as strings so JSON decoding preserves UInt64 equality.
			}
			// Preserve hash equality used by side-call classification through a string projection.
			if len(rows) > 0 {
				chInsert(t, c, "request_facts", rows...)
				for _, row := range rows {
					if err := encoder.Encode(row); err != nil {
						t.Fatal(err)
					}
				}
				total += len(rows)
			}
			time.Sleep(time.Second)
			if len(rows) < 64 {
				break
			}
			last := rows[len(rows)-1]
			p.Set("after", last["started_at"].(string))
			p.Set("after_id", last["request_id"].(string))
			seek = ` AND (started_at,request_id)>({after:DateTime64(9,'UTC')},{after_id:String})`
		}
	}
	t.Logf("COPY rows=%d expected_snapshot=%d first=%s last=%s", total, bounds[0].N, bounds[0].First.Format(time.RFC3339Nano), bounds[0].Last.Format(time.RFC3339Nano))
	if uint64(total) != bounds[0].N {
		t.Fatal("live derivation changed under snapshot; copied count differs")
	}
}

func turnSearchBackfill(t *testing.T, c *clickhouseReader, statements []string, to time.Time) {
	t.Helper()
	var first []struct {
		At time.Time `json:"at"`
	}
	if err := c.query(context.Background(), `SELECT min(started_at) AS at FROM cliproxy.request_facts`, nil, &first); err != nil {
		t.Fatal(err)
	}
	for lo := first[0].At.Truncate(15 * time.Minute); lo.Before(to); lo = lo.Add(15 * time.Minute) {
		hi := lo.Add(15 * time.Minute)
		if hi.After(to) {
			hi = to
		}
		sql := statements[3]
		for k, v := range map[string]string{"from": lo.Format("2006-01-02 15:04:05.999999999"), "to": hi.Format("2006-01-02 15:04:05.999999999"), "watermark": "2100-01-01 00:00:00"} {
			sql = strings.ReplaceAll(sql, "{"+k+":DateTime64(9,'UTC')}", "toDateTime64('"+v+"',9,'UTC')")
		}
		chExec(t, c, sql)
	}
	turnSearchActivate(t, c, statements)
}
func turnSearchFindability(t *testing.T, c *clickhouseReader, to time.Time, path string) {
	t.Helper()
	rows := []archiveSession{}
	for offset := 0; ; offset += 20 {
		out, err := c.read("sessions", url.Values{"to": {to.Format(time.RFC3339Nano)}, "days": {"7"}, "limit": {"20"}, "offset": {strconv.Itoa(offset)}, "show_system": {"1"}})
		if err != nil {
			t.Fatal(err)
		}
		page := out.([]archiveSession)
		rows = append(rows, page...)
		if len(page) < 20 {
			break
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintln(f, "family\tsession_id\ttitle\tphrase\tstatus\tpages\tmax_seconds")
	type page struct {
		ids   map[string]bool
		err   error
		pages int
		max   float64
	}
	pages := map[string]page{}
	hits, empty, failures, hidden := 0, 0, 0, 0
	for _, r := range rows {
		if r.System {
			hidden++
			continue
		}
		words := strings.Fields(strings.TrimSuffix(r.Summary, "…"))
		phrase := ""
		status := "empty"
		pg := page{}
		if len(words) == 0 {
			empty++
		} else {
			n := min(6, len(words))
			frequency := len(rows) + 1
			for start := 0; start+n <= len(words); start++ {
				candidate := strings.Join(words[start:start+n], " ")
				count := 0
				for _, other := range rows {
					if !other.System && strings.Contains(strings.ToLower(other.Summary), strings.ToLower(candidate)) {
						count++
					}
				}
				if count < frequency {
					frequency = count
					phrase = candidate
				}
			}
			var ok bool
			pg, ok = pages[phrase]
			if !ok {
				pg.ids = map[string]bool{}
				for offset := 0; ; offset += 20 {
					started := time.Now()
					out, err := c.read("sessions", url.Values{"q": {phrase}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"20"}, "offset": {strconv.Itoa(offset)}})
					elapsed := time.Since(started).Seconds()
					pg.max = max(pg.max, elapsed)
					pg.pages++
					pg.err = err
					if err != nil {
						break
					}
					result := out.([]archiveSession)
					for _, match := range result {
						pg.ids[match.SessionID] = true
					}
					if len(result) < 20 {
						break
					}
				}
				pages[phrase] = pg
			}
			status = "hit"
			if pg.err != nil {
				status = pg.err.Error()
				failures++
			} else if !pg.ids[r.SessionID] {
				status = "missing"
				failures++
			} else {
				hits++
			}
		}
		family := strings.SplitN(r.SessionID, ":", 2)[0]
		fmt.Fprintf(f, "%s\t%s\t%q\t%q\t%s\t%d\t%.6f\n", family, r.SessionID, r.Summary, phrase, status, pg.pages, pg.max)
	}
	t.Logf("FINDABILITY all=%d hidden=%d visible=%d hits=%d empty=%d failures=%d distinct_terms=%d", len(rows), hidden, len(rows)-hidden, hits, empty, failures, len(pages))
	if failures > 0 {
		t.Errorf("findability failures=%d", failures)
	}
}
func turnSearchCosts(t *testing.T, c *clickhouseReader, dataset string, to time.Time, terms []string) {
	t.Helper()
	for run := 0; run < 5; run++ {
		for _, days := range []int{7, 30} {
			for index, term := range terms {
				for _, compact := range []bool{false, true} {
					round7DiskGuard(t)
					c.turnSearchChecked = time.Now()
					c.turnSearchPresent = compact
					s := &turnSearchMeasureTransport{summaryTransport{base: http.DefaultTransport}}
					c.client.Transport = s
					start := time.Now()
					out, err := c.read("sessions", url.Values{"q": {term}, "from": {to.AddDate(0, 0, -days).Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}})
					n := 0
					if err == nil {
						n = len(out.([]archiveSession))
					}
					t.Logf("COST dataset=%s run=%d days=%d term=%d compact=%t rows=%d bytes=%d seconds=%.6f matches=%d error=%v", dataset, run, days, index, compact, s.rows, s.bytes, time.Since(start).Seconds(), n, err)
					if compact && err != nil {
						t.Errorf("compact search failed %v", err)
					}
					if err == nil && ((index == 0 && n != 0) || (index > 0 && n == 0)) {
						t.Errorf("unexpected cost output term=%q n=%d", term, n)
					}
				}
			}
		}
	}
	c.client.Transport = http.DefaultTransport
	c.turnSearchChecked = time.Time{}
}
func TestTurnSearchAcceptance(t *testing.T) {
	if os.Getenv("ARCHIVE_TURN_SEARCH_ACCEPTANCE") == "" {
		t.Skip("explicit local acceptance opt-in")
	}
	c := turnSearchFixture(t)
	c.client.Transport = &turnSearchMeasureTransport{summaryTransport{base: http.DefaultTransport}}
	evidence := os.Getenv("ARCHIVE_TURN_SEARCH_EVIDENCE")
	if evidence == "" {
		t.Fatal("evidence directory required")
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	to := time.Now().UTC()
	if raw := os.Getenv("ARCHIVE_TURN_SEARCH_TO"); raw != "" {
		var err error
		to, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("FROZEN_TO %s", to.Format(time.RFC3339Nano))
	turnSearchCopyLive(t, c, to)
	chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
	statements := turnSearchSchema(t, c)
	start := time.Now()
	turnSearchBackfill(t, c, statements, to)
	t.Logf("BACKFILL dataset=live seconds=%.6f", time.Since(start).Seconds())
	turnSearchFindability(t, c, to, filepath.Join(evidence, "title-findability.tsv"))
	turnSearchCosts(t, c, "live", to, []string{"absent-turn-search-8d27e", "regretted the choice", "you"})
	turnSearchStats(t, c, "live")
}
func turnSearchStats(t *testing.T, c *clickhouseReader, dataset string) {
	var stats []map[string]any
	if err := c.query(context.Background(), `SELECT table,sum(rows) AS rows,sum(bytes_on_disk) AS disk_bytes,sum(data_uncompressed_bytes) AS raw_bytes FROM system.parts WHERE active AND database='cliproxy' GROUP BY table ORDER BY table`, nil, &stats); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(stats)
	t.Logf("STORAGE dataset=%s %s", dataset, b)
}
func TestTurnSearchSynthetic(t *testing.T) {
	if os.Getenv("ARCHIVE_TURN_SEARCH_SYNTHETIC") == "" {
		t.Skip("explicit synthetic opt-in")
	}
	c := turnSearchFixture(t)
	chExec(t, c, `ALTER TABLE cliproxy.request_facts MODIFY SETTING merge_max_block_size=256,merge_max_block_size_bytes=1048576`)
	statements := turnSearchSchema(t, c)
	// A compact prompt per twelve requests, with tool output making the mean text 11 KB.
	insert := `INSERT INTO cliproxy.request_facts (request_id,started_at,ingested_at,conversation_id,kind,client,prior_seen,input_items,last_user_text,msg_direction,msg_role,msg_type,msg_text)
 WITH intDiv(number,12) AS session,number%12 AS step,toDateTime64('2026-11-19 12:00:00',9,'UTC')-toIntervalSecond(intDiv((330000-number)*2592000,330000)) AS ts,concat('Please fix the common archive request ',if(session=27490,'rarelyfoundneedle ',''),repeat(hex(cityHash64(session)),12)) AS prompt
 SELECT concat('req-',toString(number)),ts,ts,concat('claude:scale-',toString(session)),'messages','claude-code',step>0,200,prompt,['input'],[if(step=0,'user','tool')],['message'],[if(step=0,prompt,repeat(hex(cityHash64(number)),770))] FROM numbers(%d,1000) SETTINGS max_threads=1,max_block_size=256,min_insert_block_size_rows=256,min_insert_block_size_bytes=0`
	for offset := 0; offset < 330000; offset += 1000 {
		round7DiskGuard(t)
		chExec(t, c, fmt.Sprintf(strings.ReplaceAll(insert, "number%12", "number%%12"), offset))
		if offset%10000 == 9000 {
			chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
		}
	}
	turnSearchActivate(t, c, statements)
	chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
	chExec(t, c, `OPTIMIZE TABLE cliproxy.turn_search FINAL`)
	to := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	turnSearchCosts(t, c, "synthetic", to, []string{"absent-turn-search-8d27e", "rarelyfoundneedle", "common"})
	turnSearchStats(t, c, "synthetic")
	// Insert the same production-shaped block with and without the MV, alternating sides.
	sql := fmt.Sprintf(strings.ReplaceAll(insert, "number%12", "number%%12"), 330000)
	for run := 0; run < 5; run++ {
		for _, withMV := range []bool{false, true} {
			if !withMV {
				chExec(t, c, `DETACH TABLE cliproxy.turn_search_mv`)
			} else {
				chExec(t, c, `ATTACH TABLE cliproxy.turn_search_mv`)
			}
			start := time.Now()
			chExec(t, c, sql)
			seconds := time.Since(start).Seconds()
			chExec(t, c, `SYSTEM FLUSH LOGS`)
			var logs []struct {
				Memory uint64 `json:"memory"`
				Rows   uint64 `json:"rows"`
			}
			err := c.query(context.Background(), `SELECT memory_usage AS memory,written_rows AS rows FROM system.query_log WHERE type='QueryFinish' AND startsWith(query,'INSERT INTO cliproxy.request_facts (request_id,started_at') ORDER BY event_time_microseconds DESC LIMIT 1`, nil, &logs)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("INSERT run=%d mv=%t rows=1000 seconds=%.6f rows_per_second=%.1f query_log=%+v", run, withMV, seconds, 1000/seconds, logs)
		}
	}
}
