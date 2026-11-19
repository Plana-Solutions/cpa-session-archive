package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRound7Regressions(t *testing.T) {
	c := localClickHouse(t)
	for _, file := range []string{"schema.sql", "derived.sql", "external.sql"} {
		b, err := os.ReadFile("testdata/clickhouse/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, sql := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
		}
	}
	seed := func(id string, texts, roles []string, extra map[string]any) {
		at := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
		directions, types := make([]string, len(texts)), make([]string, len(texts))
		for i := range texts {
			directions[i] = "input"
			types[i] = "message"
		}
		row := map[string]any{"request_id": id, "conversation_id": id, "started_at": at, "ingested_at": at, "kind": "messages", "input_items": 200, "last_user_text": texts[len(texts)-1], "msg_text": texts, "msg_role": roles, "msg_direction": directions, "msg_type": types}
		for k, v := range extra {
			row[k] = v
		}
		chInsert(t, c, "request_facts", row)
	}
	seed("first-with-tools", []string{"I regretted the choice and want to fix it", "tool result"}, []string{"user", "tool"}, nil)
	seed("multiline", []string{"Perform an operation\u00a0on it.\n\n## 1.\v\tConnect the archive"}, []string{"user"}, nil)
	old := time.Now().UTC().AddDate(0, 0, -15).Format("2006-01-02 15:04:05.999999999")
	seed("old-window", []string{"only thirty days finds this older prompt"}, []string{"user"}, map[string]any{"started_at": old, "ingested_at": old})
	seed("title-generator", []string{"Generate a title that will help the user recognize this T3 Code thread weeks later.", "<skill>Extra instructions</skill>"}, []string{"user", "user"}, nil)
	seed("long-policy", []string{"<current_datetime>now</current_datetime>\n<system>Immutable security policy. " + strings.Repeat("injected ", 800) + "</system>Fix the archive titles"}, []string{"user"}, nil)
	seed("summary-policy", []string{"Some of the conversation history has been summarized to free up context.\n<user_message><current_datetime>now</current_datetime><system>Policy only</user_message>\n<user_message><system>Policy</system>Keep the actual task</user_message>\n<summary>old</summary>"}, []string{"user"}, nil)
	seed("real-mention", []string{"Please check Generate a title that will help the user recognize this T3 Code thread"}, []string{"user"}, nil)
	for _, v := range []struct{ term, id string }{{"regretted the choice", "first-with-tools"}, {"operation on it. ## 1. Connect", "multiline"}, {"operation\non it.  ## 1.\tConnect", "multiline"}} {
		t.Run(v.id+v.term, func(t *testing.T) {
			got, err := c.read("sessions", url.Values{"q": {v.term}})
			if err != nil {
				t.Fatal(err)
			}
			rows := got.([]archiveSession)
			if len(rows) != 1 || rows[0].SessionID != v.id {
				t.Fatalf("search %q got %+v", v.term, rows)
			}
		})
	}
	t.Run("bounded display strips long preambles", func(t *testing.T) {
		got, err := c.read("sessions", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got.([]archiveSession) {
			want := map[string]string{"long-policy": "Fix the archive titles", "summary-policy": "Keep the actual task"}[r.SessionID]
			if want != "" && r.Summary != want {
				t.Errorf("got %q want %q", r.Summary, want)
			}
		}
	})
	t.Run("seven day default can be widened", func(t *testing.T) {
		for _, days := range []string{"", "30"} {
			q := url.Values{"q": {"finds this older prompt"}}
			if days != "" {
				q.Set("days", days)
			}
			got, err := c.read("sessions", q)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if days == "30" {
				want = 1
			}
			if len(got.([]archiveSession)) != want {
				t.Errorf("days=%q got=%+v", days, got)
			}
		}
	})
	t.Run("system items", func(t *testing.T) {
		got, err := c.read("sessions", nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range got.([]archiveSession) {
			if r.SessionID == "title-generator" {
				t.Error("title generator visible")
			}
			found = found || r.SessionID == "real-mention"
		}
		if !found {
			t.Error("real session hidden")
		}
	})
	t.Run("missing session while continuing record", func(t *testing.T) {
		for _, q := range []url.Values{{"scope": {"all"}, "session_offset": {"100"}, "byte_offset": {"1"}}, {"id": {"deleted"}, "byte_offset": {"1"}}, {"id": {"multiline"}, "offset": {"100"}, "byte_offset": {"1"}}} {
			if got, err := c.read("export", q); err == nil {
				t.Errorf("partial export silently ended %+v", got)
			}
		}
	})

	t.Run("partial record cannot finish after session disappears", func(t *testing.T) {
		at := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
		chInsert(t, c, "external_messages", map[string]any{"session_id": "disappearing", "ts": at, "position": 0, "role": "user", "type": "message", "text": strings.Repeat("x", rawChunk*2)})
		q := url.Values{"scope": {"all"}, "id": {"external:disappearing"}, "format": {"sft"}}
		first, err := c.read("export", q)
		if err != nil {
			t.Fatal(err)
		}
		ticket := first.(map[string]any)
		if ticket["next_byte_offset"].(int) == 0 || json.Valid([]byte(ticket["content"].(string))) {
			t.Fatal("fixture did not stop mid-record")
		}
		q.Set("from", ticket["from"].(string))
		q.Set("to", ticket["to"].(string))
		q.Set("byte_offset", strconv.Itoa(ticket["next_byte_offset"].(int)))
		chExec(t, c, `TRUNCATE TABLE cliproxy.external_messages`)
		w, err := windowFor(q, 365)
		if err != nil {
			t.Fatal(err)
		}
		baseline, err := c.exportBaseline(context.Background(), w, q)
		if err != nil {
			t.Fatal(err)
		}
		if baseline.(map[string]any)["done"] != true {
			t.Fatal("did not reproduce the old silent completion")
		}
		if _, err = c.read("export", q); err == nil {
			t.Fatal("continued missing record reported success")
		} else {
			t.Logf("old done=true after partial JSON; fixed error=%v", err)
		}
	})

}

func TestRound7Preambles(t *testing.T) {
	for _, v := range []struct{ raw, want string }{
		{"Some of the conversation history has been summarized to free up context.\n<user_message><current_datetime>now</current_datetime><system>Policy only</user_message>\n<user_message><system>Policy</system>Keep the actual task</user_message>", "Keep the actual task"},
		{"Some of the conversation history has been summarized to free up context.\nYou were originally given instructions. Here were the user messages:\n<user_message>\nFix the archive titles\n</user_message>\nHere is a summary: private", "Fix the archive titles"},
		{"<system>Immutable security policy. " + strings.Repeat("injected ", 800) + "</system>\nFix the archive titles", "Fix the archive titles"},
		{"Some of the conversation history has been summarized to free up context.\n<user_message><system>Immutable security policy.</system>Fix the archive titles</user_message>\n<summary>old</summary>", "Fix the archive titles"},
		{"Some of the conversation history has been summarized to free up context.", ""},
		{"<system>Unclosed policy", ""},
		{"Discuss <system> tags in the docs", "Discuss <system> tags in the docs"},
	} {
		if got := meaningfulUserText(v.raw); got != v.want {
			t.Errorf("title got %q want %q", got, v.want)
		}
		if got := cleanTurnText(v.raw); got != v.want {
			t.Errorf("turn got %q want %q", got, v.want)
		}
	}
}

func round7LiveReader(t *testing.T) *clickhouseReader {
	t.Helper()
	if os.Getenv("ARCHIVE_R7_LIVE") == "" {
		t.Skip("opt-in read-only production checks")
	}
	return &clickhouseReader{config: pluginConfig{URL: os.Getenv("CLICKHOUSE_URL"), Database: "cliproxy", User: os.Getenv("CLICKHOUSE_USER"), Password: os.Getenv("CLICKHOUSE_PASSWORD")}, client: &http.Client{Timeout: 60 * time.Second}}
}

func TestRound7LiveTitles(t *testing.T) {
	c := round7LiveReader(t)
	cutoff := os.Getenv("ARCHIVE_R7_TO")
	if cutoff == "" {
		cutoff = time.Now().UTC().Format(time.RFC3339Nano)
	}
	t.Logf("FROZEN_TO %s", cutoff)
	out, err := c.read("sessions", url.Values{"to": {cutoff}, "days": {"7"}, "limit": {"1000"}, "show_system": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.([]archiveSession)
	if path := os.Getenv("ARCHIVE_R7_SNAPSHOT"); path != "" {
		b, _ := json.Marshal(rows)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("snapshot sessions=%d", len(rows))
		return
	}
	for _, term := range []string{"regretted the choice", "kegt to do", "gimme the url", "operation on it. ## 1. Connect"} {
		out, err := c.read("sessions", url.Values{"q": {term}, "limit": {"1000"}})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range out.([]archiveSession) {
			ids = append(ids, r.SessionID)
		}
		t.Logf("EXAMPLE term=%q ids=%v", term, ids)
		if len(ids) == 0 {
			t.Errorf("example missing %q", term)
		}
		time.Sleep(time.Second)
	}
	if path := os.Getenv("ARCHIVE_R7_AFTER"); path != "" {
		b, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]string{}
	if path := os.Getenv("ARCHIVE_R7_BEFORE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var old []archiveSession
		if err = json.Unmarshal(b, &old); err != nil {
			t.Fatal(err)
		}
		for _, r := range old {
			before[r.SessionID] = r.Summary
		}
	}
	type searchPage struct {
		rows []archiveSession
		err  error
	}
	pages := map[string]searchPage{}
	only := map[string]bool{}
	if raw := os.Getenv("ARCHIVE_R7_ONLY_IDS"); raw != "" {
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			only[id] = true
		}
	}
	hits, misses, empty, changed, system := 0, 0, 0, 0, 0
	for index, r := range rows {
		if old, ok := before[r.SessionID]; ok && old != r.Summary {
			changed++
		}
		if r.System {
			system++
			continue
		}
		if len(only) > 0 && !only[r.SessionID] {
			continue
		}
		words := strings.Fields(strings.TrimSuffix(r.Summary, "…"))
		if len(words) == 0 {
			empty++
			t.Logf("FIND id=%s empty title", r.SessionID)
			continue
		}
		phrase := strings.Join(words[:min(6, len(words))], " ")
		frequency := len(rows) + 1
		for start := 0; start+min(6, len(words)) <= len(words); start++ {
			candidate := strings.Join(words[start:start+min(6, len(words))], " ")
			count := 0
			for _, other := range rows {
				if strings.Contains(strings.ToLower(other.Summary), strings.ToLower(candidate)) {
					count++
				}
			}
			if count < frequency {
				phrase = candidate
				frequency = count
			}
		}
		offset := 0
		for _, earlier := range rows[:index] {
			if !earlier.System && strings.Contains(strings.ToLower(earlier.Summary), strings.ToLower(phrase)) {
				offset++
			}
		}
		pageOffset := offset / 20 * 20
		key := phrase + "\n" + strconv.Itoa(pageOffset)
		page, ok := pages[key]
		if !ok {
			out, err := c.read("sessions", url.Values{"q": {phrase}, "limit": {"20"}, "offset": {strconv.Itoa(pageOffset)}, "to": {cutoff}})
			page.err = err
			if err == nil {
				page.rows = out.([]archiveSession)
			}
			pages[key] = page
			t.Logf("SEARCH_PAGE phrase=%q offset=%d rows=%d error=%v", phrase, pageOffset, len(page.rows), err)
			time.Sleep(time.Second)
		}
		found := false
		for _, v := range page.rows {
			found = found || v.SessionID == r.SessionID
		}
		err := page.err

		if found {
			hits++
		} else {
			misses++
		}
		t.Logf("FIND id=%s phrase=%q hit=%t offset=%d error=%v", r.SessionID, phrase, found, offset, err)
		if !found && err == nil {
			t.Logf("MISSING_RESULT id=%s returned=%d", r.SessionID, len(page.rows))
		}
	}
	t.Logf("FIND_TOTAL sessions=%d system=%d hits=%d misses=%d empty=%d changed=%d", len(rows), system, hits, misses, empty, changed)
	// Independently inspect all user-message items, including ones after the display bound.
	w, _ := windowFor(url.Values{"days": {"7"}, "to": {cutoff}}, 7)
	var generators []struct {
		ID string `json:"id"`
	}
	err = c.query(context.Background(), `SELECT DISTINCT conversation_id AS id FROM cliproxy.request_facts WHERE `+timeFilter+` AND arrayExists((x,d,r,t)->d='input' AND r='user' AND t='message' AND startsWith(x,'Generate a title that will help the user recognize this T3 Code thread'),msg_text,msg_direction,msg_role,msg_type)`, w.params, &generators)
	if err != nil {
		t.Fatal(err)
	}
	hidden := 0
	for _, g := range generators {
		for _, r := range rows {
			if r.SessionID == g.ID && r.System {
				hidden++
			}
		}
	}
	t.Logf("SYSTEM generators=%d hidden=%d", len(generators), hidden)
	if misses > 0 || empty > 0 || hidden != len(generators) {
		t.Fail()
	}
}

func TestRound7SearchCapacity(t *testing.T) {
	if os.Getenv("ARCHIVE_R7_COST") == "" {
		t.Skip("opt-in capacity measurement")
	}
	var c *clickhouseReader
	live := os.Getenv("ARCHIVE_R7_LIVE") != ""
	if live {
		c = round7LiveReader(t)
	} else {
		c = localClickHouse(t)
		for _, file := range []string{"schema.sql", "derived.sql"} {
			b, err := os.ReadFile("testdata/clickhouse/" + file)
			if err != nil {
				t.Fatal(err)
			}
			for _, sql := range strings.Split(string(b), "\n;\n") {
				chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
			}
		}
		chExec(t, c, `ALTER TABLE cliproxy.request_facts MODIFY SETTING merge_max_block_size=256,merge_max_block_size_bytes=1048576`)
		for offset := 0; offset < 330000; offset += 1000 {
			round7DiskGuard(t)
			chExec(t, c, fmt.Sprintf(`INSERT INTO cliproxy.request_facts (request_id,started_at,ingested_at,conversation_id,kind,client,prior_seen,input_items,last_user_text,msg_direction,msg_role,msg_type,msg_text)
WITH intDiv(number,12) AS session,number%%12 AS step,toDateTime64('2026-11-19 12:00:00',9,'UTC')-toIntervalSecond(intDiv((330000-number)*2592000,330000)) AS ts,concat('Please fix this archive request ',repeat(hex(cityHash64(session)),710)) AS prompt
SELECT concat('req-',toString(number)),ts,ts,concat('claude:scale-',toString(session)),'messages','claude-code',step>0,200,prompt,['input'],[if(step=0,'user','tool')],['message'],[if(step=0,prompt,repeat(hex(cityHash64(number)),712))] FROM numbers(%d,1000) SETTINGS max_threads=1,max_block_size=1000,min_insert_block_size_rows=1000,min_insert_block_size_bytes=0`, offset))
			if offset%10000 == 9000 {
				chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
			}
		}
		chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
	}
	to := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	if live {
		to = time.Now().UTC()
	}
	for i := 0; i < 5; i++ {
		for _, days := range []int{7, 30} {
			if !live {
				round7DiskGuard(t)
			}
			s := &round6Transport{summaryTransport: summaryTransport{base: http.DefaultTransport}}
			c.client.Transport = s
			q := url.Values{"q": {"absent-round7-needle-4ea611"}, "from": {to.AddDate(0, 0, -days).Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
			start := time.Now()
			out, err := c.read("sessions", q)
			elapsed := time.Since(start)
			n := 0
			if err == nil {
				n = len(out.([]archiveSession))
			}
			if n != 0 {
				t.Fatalf("absent search has %d matches", n)
			}
			t.Logf("CAPACITY live=%t run=%d days=%d queries=%d read_rows=%d read_bytes=%d seconds=%.6f matches=%d error=%v", live, i, days, s.queries, s.rows, s.bytes, elapsed.Seconds(), n, err)
			if !live && err != nil && err.Error() != "search timed out, narrow the range" {
				t.Fatal(err)
			}
			if live {
				time.Sleep(2 * time.Second)
			}
		}
	}
	if live {
		var count []struct {
			Rows  uint64    `json:"rows"`
			First time.Time `json:"first"`
			Last  time.Time `json:"last"`
		}
		w, err := windowFor(url.Values{"from": {to.AddDate(0, 0, -30).Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}, 30)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.query(context.Background(), `SELECT count() AS rows,min(started_at) AS first,max(started_at) AS last FROM cliproxy.request_facts WHERE `+timeFilter, w.params, &count); err != nil {
			t.Fatal(err)
		}
		t.Logf("LIVE_DATA %+v", count)
	}
	if !live {
		var rows []struct {
			Rows      uint64 `json:"rows"`
			Bytes     uint64 `json:"bytes"`
			TextBytes uint64 `json:"text_bytes"`
		}
		err := c.query(context.Background(), `SELECT count() AS rows,sum(length(arrayStringConcat(msg_text))) AS text_bytes FROM cliproxy.request_facts`, nil, &rows)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("FIXTURE %+v", rows)
		var parts []struct {
			Bytes uint64 `json:"bytes"`
		}
		if err = c.query(context.Background(), `SELECT sum(bytes_on_disk) AS bytes FROM system.parts WHERE active AND database='cliproxy'`, nil, &parts); err != nil {
			t.Fatal(err)
		}
		t.Logf("DISK %+v", parts)
		if parts[0].Bytes > 1500000000 {
			t.Fatal("fixture exceeded disk budget")
		}
	}
}

func round7DiskGuard(t *testing.T) {
	t.Helper()
	out, err := exec.Command("df", "-Pk", os.TempDir()).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	free, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if free*1024 < 2500000000 {
		t.Fatal("stop below 2.5 GB free")
	}
}

func TestRound7LiveCompactionTitle(t *testing.T) {
	c := round7LiveReader(t)
	w, err := windowFor(url.Values{"days": {"7"}}, 7)
	if err != nil {
		t.Fatal(err)
	}
	id := "4f785046-1f9f-47a8-b924-7491e9a97f6a"
	texts, err := c.displayTexts(context.Background(), w, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := firstUserText(texts[id].Texts)
	if !strings.HasPrefix(title, "# Merge an upstream T3 release into the Cloud fork") {
		t.Fatalf("live compacted title did not retain the task, length=%d", len(title))
	}
	t.Log("live compacted title retained the actual task after a truncated policy-only message")
}
