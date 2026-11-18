package archive

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Export chunks are UTF-8 slices of complete JSONL records. The client concatenates them without parsing partial records.
// Snapshot of 494780b for opt-in before/after resource checks.
func (c *clickhouseReader) exportBaseline(ctx context.Context, w readWindow, q url.Values) (any, error) {
	if now := time.Now().UTC(); w.to.After(now) {
		w.to = now
		w.params.Set("to", now.Format("2006-01-02 15:04:05.999999999"))
	}
	format := q.Get("format")
	if format == "" {
		format = "archive"
	}
	if format != "archive" && format != "sft" {
		return nil, badQuery("unsupported export format")
	}
	if q.Get("scope") != "all" && q.Get("id") == "" {
		return nil, badQuery("session id is required")
	}
	offset, err := boundedInt(q, "offset", 0, 1000000)
	if err != nil {
		return nil, err
	}
	part, err := boundedInt(q, "byte_offset", 0, 1<<30)
	if err != nil {
		return nil, err
	}
	w.params.Set("offset", strconv.Itoa(offset))
	w.params.Set("part", strconv.Itoa(part+1))
	where := timeFilter + ` AND conversation_id!='' AND ` + conversationTraffic
	if q.Get("id") != "" {
		w.params.Set("session", q.Get("id"))
		where += ` AND conversation_id={session:String}`
	}
	sessionsSQL := `SELECT conversation_id AS id FROM {db:Identifier}.request_facts WHERE ` + where + ` GROUP BY conversation_id`
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if present {
		extWhere := `ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')}`
		if q.Get("id") != "" {
			w.params.Set("external_session", strings.TrimPrefix(q.Get("id"), "external:"))
			extWhere += ` AND session_id={external_session:String}`
		}
		sessionsSQL += ` UNION ALL SELECT concat('external:',session_id) AS id FROM {db:Identifier}.external_messages FINAL WHERE ` + extWhere + ` GROUP BY session_id`
	}
	sessionIndex, err := boundedInt(q, "session_offset", 0, 1000000)
	if err != nil {
		return nil, err
	}
	w.params.Set("session_offset", strconv.Itoa(sessionIndex))
	var sessions []struct {
		ID string `json:"id"`
	}
	err = c.query(ctx, `SELECT id FROM (`+sessionsSQL+`) ORDER BY id LIMIT 1 OFFSET {session_offset:UInt32}`, w.params, &sessions)
	if err != nil {
		return nil, err
	}
	name := "session." + format + ".jsonl"
	if q.Get("scope") == "all" {
		name = "cpa-session-archive.all." + format + ".jsonl"
	}
	ticket := map[string]any{"filename": name, "content_type": "application/x-ndjson", "content": "", "done": len(sessions) == 0, "next_offset": offset, "next_byte_offset": 0, "next_session_offset": sessionIndex, "from": w.from.Format(time.RFC3339Nano), "to": w.to.Format(time.RFC3339Nano)}
	if len(sessions) == 0 {
		return ticket, nil
	}
	id := sessions[0].ID
	w.params.Set("session", strings.TrimPrefix(id, "external:"))
	var recordsSQL string
	if strings.HasPrefix(id, "external:") {
		base := `FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} AND session_id={session:String}`
		if format == "sft" {
			recordsSQL = `SELECT toJSONString(map('messages',arrayMap(x->map('role',x.2,'content',x.3),arraySort(x->x.1,groupArray((position,role,text)))))) AS line ` + base + ` HAVING count()>0 LIMIT 1 OFFSET {offset:UInt32}`
		} else {
			recordsSQL = `SELECT toJSONString(CAST((concat('external:',session_id,':',toString(position)),concat('external:',session_id),'external-native-event',base64Encode(toJSONString(map('role',role,'text',text))),''),'Tuple(request_id String,session_id String,source_format String,original_request String,response String)')) AS line ` + base + ` ORDER BY position LIMIT 1 OFFSET {offset:UInt32}`
		}
	} else if format == "sft" {
		recordsSQL = `SELECT concat('{"messages":[',arrayStringConcat(arrayMap(x->x.3,arraySort(x->(x.1,x.2),groupArray((started_at,sort_key,message)))),','),']}') AS line FROM (
 SELECT started_at,concat(request_id,if(direction='input','0','1'),leftPad(toString(position),10,'0')) AS sort_key,
 multiIf(role='tool',toJSONString(CAST(('tool',call_id,text),'Tuple(role String,tool_call_id String,content String)')),
 role='assistant' AND tool_name!='',toJSONString(CAST(('assistant',[(call_id,'function',(tool_name,arguments))]),'Tuple(role String,tool_calls Array(Tuple(id String,type String,function Tuple(name String,arguments String))))')),
 toJSONString(map('role',role,'content',text))) AS message
 FROM {db:Identifier}.messages WHERE ` + timeFilter + ` AND conversation_id={session:String} AND ` + conversationTraffic + `) HAVING count()>0 LIMIT 1 OFFSET {offset:UInt32}`

	} else {
		recordsSQL = numberedTurns + `SELECT toJSONString(CAST((r.request_id,{session:String},r.trace_id,r.model,r.requested_model,r.source_format,r.outcome,r.status_code,r.started_at,r.completed_at,base64Encode(r.request_body),base64Encode(if(r.response_body!='',r.response_body,arrayStringConcat(r.response_chunks,'')))),'Tuple(request_id String,session_id String,trace_id String,model String,requested_model String,source_format String,outcome String,status_code UInt16,started_at DateTime64(9,\'UTC\'),completed_at DateTime64(9,\'UTC\'),original_request String,response String)')) AS line FROM {db:Identifier}.requests AS r WHERE ` + timeFilter + ` AND request_id IN (SELECT argMax(request_id,(started_at,request_id)) FROM grouped GROUP BY turn_key) ORDER BY started_at,request_id LIMIT 1 OFFSET {offset:UInt32}`
	}
	var rows []struct {
		Content string `json:"content"`
		Length  int    `json:"length"`
	}
	err = c.query(ctx, `SELECT substringUTF8(line,{part:UInt32},1048576) AS content,lengthUTF8(line) AS length FROM (`+recordsSQL+`) LIMIT 1`, w.params, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		ticket["next_session_offset"] = sessionIndex + 1
		ticket["next_offset"] = 0
		return ticket, nil
	}
	ticket["content"] = rows[0].Content
	if part+1048576 >= rows[0].Length {
		ticket["content"] = rows[0].Content + "\n"
		ticket["next_offset"] = offset + 1
	} else {
		ticket["next_byte_offset"] = part + 1048576
	}
	return ticket, nil
}
