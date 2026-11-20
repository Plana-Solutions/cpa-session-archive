package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRound11SystemAndAutomation(t *testing.T) {
	c := turnSearchFixture(t)
	statements := turnSearchSchema(t, c)
	at := time.Now().UTC().Add(-time.Hour)
	seed := func(id, session, text string, extra map[string]any) {
		row := map[string]any{"request_id": id, "conversation_id": session, "started_at": at.Format("2006-01-02 15:04:05.999999999"), "kind": "messages", "input_items": 200, "msg_direction": []string{"input"}, "msg_role": []string{"user"}, "msg_type": []string{"message"}, "msg_text": []string{text}}
		for k, v := range extra {
			row[k] = v
		}
		chInsert(t, c, "request_facts", row)
		at = at.Add(time.Second)
	}
	seed("human", "ordinary", "Fix the notification labels", nil)
	for i, text := range []string{
		"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\n- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.",
		"Delegated task node:delegated-task:command%3Amcp%3Adelegate-task (Audit) [completed]:\nThere are few real findings.",
		"<task-notification>\n<task-id>abc</task-id>\n<status>completed</status>\n<summary>Background command completed</summary>\n</task-notification>",
		"<system-reminder>Client context without a human prompt</system-reminder>",
	} {
		seed(fmt.Sprint("auto", i), "ordinary", text, nil)
	}
	seed("hidden-prompt", "hidden", "Fix the notification labels", nil)
	seed("hidden-tool", "hidden", "tool result", map[string]any{"msg_role": []string{"tool"}, "thread_source": "system", "prior_seen": true})
	seed("hidden-title", "title", "Generate a title that will help the user recognize this T3 Code thread", nil)
	seed("hidden-compact", "compact", "summary", map[string]any{"kind": "compact"})
	seed("hidden-kind", "kind", "summary", map[string]any{"request_kind": "compaction"})
	seed("hidden-suffix", "thread:compaction", "summary", nil)
	var oracle, actual []struct {
		ID     string `json:"id"`
		System bool   `json:"system"`
	}
	if err := c.query(context.Background(), `SELECT request_id AS id,CAST(`+systemSession+`,'Bool') AS system FROM cliproxy.request_facts ORDER BY id`, nil, &oracle); err != nil {
		t.Fatal(err)
	}
	if err := c.query(context.Background(), `SELECT request_id AS id,system FROM cliproxy.turn_search ORDER BY id`, nil, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oracle, actual) {
		t.Fatalf("stored classification differs: %v %v", oracle, actual)
	}
	for _, compact := range []bool{false, true} {
		c.turnSearchChecked = time.Now()
		c.turnSearchPresent = compact
		out, err := c.read("sessions", url.Values{"q": {"notification labels"}})
		if err != nil || len(out.([]archiveSession)) != 1 || out.([]archiveSession)[0].SessionID != "ordinary" {
			t.Fatalf("hidden check compact=%t %v %v", compact, out, err)
		}
		out, err = c.read("sessions", nil)
		if err != nil || len(out.([]archiveSession)) != 1 {
			t.Fatalf("list compact=%t %v %v", compact, out, err)
		}
	}
	turnSearchActivate(t, c, statements)
	summary := &summaryTransport{base: http.DefaultTransport}
	c.client.Transport = summary
	if _, err := c.read("sessions", nil); err != nil {
		t.Fatal(err)
	}
	before := summary.bytes
	summary.bytes = 0
	seed("large-tool", "ordinary", strings.Repeat("unrelated tool output", 100000), map[string]any{"prior_seen": true, "msg_role": []string{"tool"}})
	summary.bytes = 0
	if _, err := c.read("sessions", nil); err != nil {
		t.Fatal(err)
	}
	if summary.bytes > before+50000 {
		t.Fatalf("list cost grew with tool output: before=%d after=%d", before, summary.bytes)
	}
	out, err := c.read("turns", url.Values{"session_id": {"ordinary"}})
	if err != nil {
		t.Fatal(err)
	}
	turns := out.(map[string]any)["turns"].([]clickhouseTurn)
	if len(turns) != 5 || turns[0].Automated || turns[0].UserText != "Fix the notification labels" {
		t.Fatalf("turns %+v", turns)
	}
	for _, turn := range turns[1:] {
		if !turn.Automated || turn.UserText != "" {
			t.Fatalf("automated turn shown as prompt %+v", turn)
		}
	}
}

func TestRound11Acceptance(t *testing.T) {
	if os.Getenv("ARCHIVE_ROUND11_ACCEPTANCE") == "" {
		t.Skip("explicit narrow live-copy opt-in")
	}
	c := turnSearchFixture(t)
	to, err := time.Parse(time.RFC3339Nano, os.Getenv("ARCHIVE_TURN_SEARCH_TO"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FROZEN_TO %s", to.Format(time.RFC3339Nano))
	turnSearchCopyLive(t, c, to)
	chExec(t, c, `OPTIMIZE TABLE cliproxy.request_facts FINAL`)
	statements := turnSearchSchema(t, c)
	turnSearchBackfill(t, c, statements, to)
	if os.Getenv("ARCHIVE_ROUND11_COPY_ONLY") != "" {
		return
	}
	turnSearchFindability(t, c, to, filepath.Join(os.Getenv("ARCHIVE_TURN_SEARCH_EVIDENCE"), "title-findability.tsv"))
	var sessions []struct {
		ID string `json:"id"`
	}
	if err := c.query(context.Background(), `SELECT DISTINCT conversation_id AS id FROM cliproxy.request_facts WHERE startsWith(conversation_id,'claude:5fbcb359') AND position(conversation_id,':agent:')=0`, nil, &sessions); err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		out, err := c.read("turns", url.Values{"session_id": {session.ID}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"1000"}})
		if err != nil {
			t.Fatal(err)
		}
		turns := out.(map[string]any)["turns"].([]clickhouseTurn)
		automated, blank := 0, 0
		for _, turn := range turns {
			if turn.Automated {
				automated++
			}
			if turn.UserText == "" && !turn.Automated {
				blank++
			}
			if strings.HasPrefix(turn.UserText, "CRITICAL: Respond") {
				t.Fatal("compaction exposed as user prompt")
			}
		}
		t.Logf("CLAUDE_SESSION turns=%d automated=%d unexplained_blank=%d", len(turns), automated, blank)
		if automated < 2 || blank != 0 {
			t.Fatal("live-shaped automated turns not exercised")
		}
	}
	if len(sessions) == 0 {
		t.Fatal("required Claude session missing")
	}
	round11Costs(t, c, to)
}

func round11Costs(t *testing.T, c *clickhouseReader, to time.Time) {
	s := &turnSearchMeasureTransport{summaryTransport{base: http.DefaultTransport}}
	c.client.Transport = s
	w, err := windowFor(url.Values{"to": {to.Format(time.RFC3339Nano)}, "days": {"30"}}, 30)
	if err != nil {
		t.Fatal(err)
	}
	var ids []struct {
		ID string `json:"id"`
	}
	if err := c.query(context.Background(), `SELECT conversation_id AS id FROM cliproxy.request_facts GROUP BY conversation_id ORDER BY max(started_at) DESC LIMIT 80`, nil, &ids); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, id := range ids {
		keys = append(keys, id.ID)
	}
	encoded, _ := json.Marshal(keys)
	w.params.Set("system_candidate_ids", string(encoded))
	var priorList, priorHidden string
	for run := 0; run < 5; run++ {
		for _, compact := range []bool{false, true} {
			c.turnSearchChecked = time.Now()
			c.turnSearchPresent = compact
			s.bytes = 0
			s.rows = 0
			start := time.Now()
			out, err := c.read("sessions", url.Values{"to": {to.Format(time.RFC3339Nano)}, "days": {"30"}})
			elapsed := time.Since(start).Seconds()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(out)
			if priorList != "" && priorList != string(b) {
				t.Fatal("list changed")
			}
			priorList = string(b)
			t.Logf("ROUND11_COST operation=list compact=%t run=%d seconds=%.6f rows=%d bytes=%d matches=%d", compact, run, elapsed, s.rows, s.bytes, len(out.([]archiveSession)))
			table, rule, traffic := "request_facts", systemSession, ` AND `+conversationTraffic
			if compact {
				table, rule, traffic = "turn_search", "system", ""
			}
			sql := `SELECT conversation_id AS id FROM cliproxy.` + table + ` PREWHERE ` + timeFilter + ` AND conversation_id IN (SELECT arrayJoin(JSONExtract({system_candidate_ids:String},'Array(String)')))` + traffic + ` GROUP BY conversation_id HAVING max(` + rule + `) ORDER BY id`
			s.bytes = 0
			s.rows = 0
			start = time.Now()
			var hidden []struct {
				ID string `json:"id"`
			}
			if err := c.query(context.Background(), sql, w.params, &hidden); err != nil {
				t.Fatal(err)
			}
			elapsed = time.Since(start).Seconds()
			b, _ = json.Marshal(hidden)
			if priorHidden != "" && priorHidden != string(b) {
				t.Fatal("hidden classification changed")
			}
			priorHidden = string(b)
			t.Logf("ROUND11_COST operation=hidden80 compact=%t run=%d seconds=%.6f rows=%d bytes=%d matches=%d", compact, run, elapsed, s.rows, s.bytes, len(hidden))
		}
	}
	turnSearchStats(t, c, "round11-live")
}

func TestRound11DuplicateDisplay(t *testing.T) {
	c := turnSearchFixture(t)
	statements := turnSearchSchema(t, c)
	rows := []map[string]any{}
	ids := []string{}
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 8; i++ {
		id := fmt.Sprint("batch-", i)
		ids = append(ids, id)
		rows = append(rows, map[string]any{"request_id": id, "conversation_id": id, "started_at": at.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05.999999999"), "kind": "messages", "msg_direction": []string{"input"}, "msg_role": []string{"user"}, "msg_type": []string{"message"}, "msg_text": []string{"Prompt " + id}})
	}
	chInsert(t, c, "request_facts", rows...)
	chExec(t, c, `INSERT INTO cliproxy.turn_search SELECT s.* FROM cliproxy.turn_search AS s CROSS JOIN numbers(16) AS repeats WHERE request_id='batch-0'`)
	chExec(t, c, `OPTIMIZE TABLE cliproxy.turn_search FINAL`)
	turnSearchActivate(t, c, statements)
	w, err := windowFor(nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.displayTexts(context.Background(), w, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 8 {
		t.Fatalf("duplicate candidates consumed display batch: got %d IDs", len(out))
	}
	for _, id := range ids {
		if text, _ := firstUserText(out[id].Texts); text != "Prompt "+id {
			t.Fatalf("missing prompt %s", id)
		}
	}
}
