package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func turnSearchSchema(t *testing.T, c *clickhouseReader) []string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/002-turn-search.sql")
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	clean := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			clean = append(clean, line)
		}
	}
	for _, part := range strings.Split(strings.Join(clean, "\n"), ";") {
		var lines []string
		for _, line := range strings.Split(part, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				lines = append(lines, line)
			}
		}
		sql := strings.TrimSpace(strings.Join(lines, "\n"))
		if sql != "" {
			statements = append(statements, sql)
		}
	}
	for _, sql := range statements[:3] {
		chExec(t, c, sql)
	}
	return statements
}
func turnSearchActivate(t *testing.T, c *clickhouseReader, statements []string) {
	t.Helper()
	chExec(t, c, statements[4])
	c.turnSearchChecked = time.Time{}
}
func turnSearchFixture(t *testing.T) *clickhouseReader {
	t.Helper()
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
	return c
}

func TestTurnSearchMigration(t *testing.T) {
	c := turnSearchFixture(t)
	at := time.Now().UTC().Add(-time.Hour)
	seed := func(id, session, text string, extra map[string]any) {
		row := map[string]any{"request_id": id, "conversation_id": session, "started_at": at.Format("2006-01-02 15:04:05.999999999"), "ingested_at": at.Format("2006-01-02 15:04:05.999999999"), "kind": "messages", "input_items": 200, "msg_direction": []string{"input"}, "msg_role": []string{"user"}, "msg_type": []string{"message"}, "msg_text": []string{text}}
		for k, v := range extra {
			row[k] = v
		}
		chInsert(t, c, "request_facts", row)
		at = at.Add(time.Second)
	}
	seed("first", "ordinary", "First genuine prompt", map[string]any{"system_prompt_hash": 10})
	absent, err := c.read("sessions", url.Values{"q": {"genuine prompt"}})
	if err != nil || len(absent.([]archiveSession)) != 1 {
		t.Fatalf("absent fallback %v %v", absent, err)
	}
	statements := turnSearchSchema(t, c)
	source := strings.SplitN(statements[1], " AS\n", 2)[1]
	mv := strings.SplitN(statements[2], " AS\n", 2)[1]
	if source != mv {
		t.Fatal("MV and backfill must use the same projection")
	}

	if present, err := c.turnSearchExists(context.Background()); err != nil || present {
		t.Fatalf("building table enabled %t %v", present, err)
	}
	// Same rows exercise the MV and the reader's original window-relative rule.
	seed("followup", "ordinary", "Second genuine prompt", map[string]any{"prior_seen": true, "system_prompt_hash": 10})
	seed("side", "ordinary", "side-only-needle", map[string]any{"prior_seen": true, "input_items": 2, "system_prompt_hash": 20, "msg_direction": []string{"input", "input"}, "msg_role": []string{"system", "user"}, "msg_type": []string{"message", "message"}, "msg_text": []string{"helper policy", "side-only-needle"}})
	seed("tool", "ordinary", "tool-only-needle", map[string]any{"prior_seen": true, "msg_role": []string{"tool"}})
	seed("first-tools", "mixed", "first-with-tool-needle", map[string]any{"msg_direction": []string{"input", "input"}, "msg_role": []string{"user", "tool"}, "msg_type": []string{"message", "tool_result"}, "msg_text": []string{"first-with-tool-needle", "tool output"}})
	seed("tool-user", "ordinary", "ignored", map[string]any{"prior_seen": true, "msg_direction": []string{"input", "input"}, "msg_role": []string{"tool", "user"}, "msg_type": []string{"tool_result", "message"}, "msg_text": []string{"output", "mixed-continuation-needle"}})
	seed("count", "count", "count-only-needle", map[string]any{"kind": "count_tokens"})
	for i, v := range []struct{ raw, term string }{
		{"Look &amp; listen", "look & listen"},
		{`Literal 100% _ value \ path`, `100% _ value \ path`},
		{"A<t3_context>private</t3_context>B", "ab"},
		{"[Assistant quote](t3-citation://thread/first?comment=first+point)", "first point"},
		{"<command-name>/review</command-name><command-args>this diff</command-args>", "/review this diff"},
		{"Perform an\u00a0operation.\n\nConnect", "an operation. Connect"},
		{"<system>" + strings.Repeat("policy ", 2000) + "</system>Actual task after policy", "Actual task after policy"},
		{strings.Repeat("long genuine prompt ", 5000) + "beyond-cap-needle", "beyond-cap-needle"},
	} {
		seed(fmt.Sprint("clean-", i), fmt.Sprint("clean-", i), v.raw, nil)
	}
	sharedTerms := []string{}
	for i, v := range firstUserTextFixtures {
		roles, directions, types := []string{}, []string{}, []string{}
		for range v.texts {
			roles = append(roles, "user")
			directions = append(directions, "input")
			types = append(types, "message")
		}
		seed(fmt.Sprint("shared-", i), fmt.Sprint("shared-", i), "", map[string]any{"msg_text": v.texts, "msg_role": roles, "msg_direction": directions, "msg_type": types})
		if v.want != "" {
			sharedTerms = append(sharedTerms, strings.SplitN(v.want, "\n", 2)[0])
		}
	}
	chInsert(t, c, "external_messages", map[string]any{"session_id": "imported", "ts": at.Format("2006-01-02 15:04:05.999999999"), "position": 0, "role": "user", "type": "message", "text": "imported genuine prompt", "title": "Native title"}, map[string]any{"session_id": "imported", "ts": at.Format("2006-01-02 15:04:05.999999999"), "position": 1, "role": "assistant", "type": "message", "text": "assistant-only-needle"})
	// Backfill reuses the same SQL as the MV. Duplicates cannot change search pages.
	from := at.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.999999999")
	to := at.Add(time.Hour).Format("2006-01-02 15:04:05.999999999")
	backfill := statements[3]
	for k, v := range map[string]string{"from": from, "to": to, "watermark": to} {
		backfill = strings.ReplaceAll(backfill, "{"+k+":DateTime64(9,'UTC')}", "toDateTime64('"+v+"',9,'UTC')")
	}
	chExec(t, c, backfill)
	var covered []struct {
		ID string `json:"id"`
	}
	if err := c.query(context.Background(), `SELECT DISTINCT request_id AS id FROM cliproxy.turn_search`, nil, &covered); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range covered {
		ids[r.ID] = true
	}
	for _, id := range []string{"first", "followup", "side", "first-tools", "clean-7"} {
		if !ids[id] {
			t.Errorf("missing candidate %s", id)
		}
	}
	for _, id := range []string{"tool", "tool-user", "count"} {
		if ids[id] {
			t.Errorf("non-turn candidate %s", id)
		}
	}
	terms := []string{`100% _ value \ path`, "genuine prompt", "side-only-needle", "tool-only-needle", "first-with-tool-needle", "mixed-continuation-needle", "count-only-needle", "look & listen", "ab", "first point", "/review this diff", "an operation. Connect", "Actual task after policy", "beyond-cap-needle", "Native title", "assistant-only-needle"}
	terms = append(terms, sharedTerms...)
	for _, term := range terms {
		c.turnSearchChecked = time.Now()
		c.turnSearchPresent = false
		before, err := c.read("sessions", url.Values{"q": {term}})
		if err != nil {
			t.Fatal(err)
		}
		turnSearchActivate(t, c, statements)
		after, err := c.read("sessions", url.Values{"q": {term}})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if string(a) != string(b) {
			t.Errorf("%q fallback=%s compact=%s", term, a, b)
		}
	}
	for i := 0; i < 35; i++ {
		seed(fmt.Sprint("page-", i), fmt.Sprint("page-", i), "pagination-shared-needle ordinary", nil)
	}
	for i := 0; i < 12; i++ {
		seed(fmt.Sprint("hidden-", i), fmt.Sprint("hidden-", i), "Generate a title that will help the user recognize this T3 Code thread pagination-shared-needle", nil)
	}
	for _, compact := range []bool{false, true} {
		c.turnSearchChecked = time.Now()
		c.turnSearchPresent = compact
		for _, v := range []struct{ offset, want string }{{"0", "20"}, {"20", "15"}} {
			got, err := c.read("sessions", url.Values{"q": {"pagination-shared-needle"}, "limit": {"20"}, "offset": {v.offset}})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(len(got.([]archiveSession))) != v.want {
				t.Fatalf("compact=%t offset=%s page=%+v", compact, v.offset, got)
			}
		}
	}
	// Inserting after activation must reach the table without a manual refresh.
	seed("later", "later", "freshly-inserted-needle", nil)
	var fresh []struct {
		N uint64 `json:"n"`
	}
	if err := c.query(context.Background(), `SELECT count() AS n FROM cliproxy.turn_search WHERE request_id='later'`, nil, &fresh); err != nil || len(fresh) != 1 || fresh[0].N != 1 {
		t.Fatalf("MV insert %v %v", fresh, err)
	}
}

func TestTurnSearchDeepPages(t *testing.T) {
	c := turnSearchFixture(t)
	statements := turnSearchSchema(t, c)
	at := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
	rows := []map[string]any{}
	expected := []string{}
	for i := 0; i < 180; i++ {
		text := "yo ordinary"
		if i%6 == 0 {
			text = "Generate a title that will help the user recognize this T3 Code thread yo"
		}
		id := fmt.Sprintf("page-%03d", i)
		if i%6 != 0 {
			expected = append(expected, id)
		}
		rows = append(rows, map[string]any{"request_id": id, "conversation_id": id, "started_at": at, "ingested_at": at, "kind": "messages", "input_items": 200, "msg_direction": []string{"input"}, "msg_role": []string{"user"}, "msg_type": []string{"message"}, "msg_text": []string{text}})
	}
	chInsert(t, c, "request_facts", rows...)
	turnSearchActivate(t, c, statements)
	for _, compact := range []bool{false, true} {
		c.turnSearchChecked = time.Now()
		c.turnSearchPresent = compact
		for offset := 0; offset <= 140; offset += 20 {
			got, err := c.read("sessions", url.Values{"q": {"yo"}, "limit": {"20"}, "offset": {fmt.Sprint(offset)}})
			if err != nil {
				t.Fatal(err)
			}
			page := got.([]archiveSession)
			want := expected[offset:min(offset+20, len(expected))]
			ids := []string{}
			for _, row := range page {
				ids = append(ids, row.SessionID)
			}
			if fmt.Sprint(ids) != fmt.Sprint(want) {
				t.Fatalf("compact=%t offset=%d got=%v want=%v", compact, offset, ids, want)
			}
		}
	}

	chExec(t, c, "ALTER TABLE cliproxy.turn_search MODIFY COMMENT 'archive-turn-search-v1-building'")
	c.turnSearchChecked = time.Now().Add(-31 * time.Second)
	if present, err := c.turnSearchExists(context.Background()); err != nil || present {
		t.Fatalf("rollback did not deactivate lookup %t %v", present, err)
	}
	for _, sql := range []string{"DROP TABLE cliproxy.turn_search_mv", "DROP VIEW cliproxy.turn_search_source", "DROP TABLE cliproxy.turn_search"} {
		chExec(t, c, sql)
	}
	got, err := c.read("sessions", url.Values{"q": {"yo"}, "offset": {"100"}})
	if err != nil || len(got.([]archiveSession)) != 20 {
		t.Fatalf("rollback fallback %v %v", got, err)
	}
}
