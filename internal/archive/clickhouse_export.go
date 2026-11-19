package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
)

// Export chunks are UTF-8 slices of complete JSONL records. The client concatenates them without parsing partial records.
func (c *clickhouseReader) export(ctx context.Context, w readWindow, q url.Values) (any, error) {
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
		if part > 0 {
			return nil, badQuery("export session disappeared while continuing a record; restart the export")
		}
		return ticket, nil
	}
	id := sessions[0].ID
	w.params.Set("session", strings.TrimPrefix(id, "external:"))
	var content []byte
	var record *serializedExport
	key := exportRecordKey{Format: format, Session: id, From: w.params.Get("from"), To: w.params.Get("to"), Offset: offset}
	endSession := false
	if strings.HasPrefix(id, "external:") {
		var rows []struct {
			externalMessage
			Version string `json:"cache_version"`
		}
		externalFilter := ""
		if format == "sft" {
			externalFilter = " AND type NOT IN ('reasoning','thinking')"
		}
		err = c.query(ctx, `SELECT `+externalMetadataColumns+`,toString(ingested_at) AS cache_version FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} AND session_id={session:String}`+externalFilter+` ORDER BY position LIMIT 1 OFFSET {offset:UInt32} SETTINGS max_threads=1,max_block_size=1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			endSession = true
		} else {
			m := rows[0].externalMessage
			key.Source = strconv.FormatUint(uint64(m.Position), 10)
			key.Started = m.TS.UnixNano()
			key.Version = rows[0].Version
			record = c.cachedExport(key)
			if record == nil {
				m.Text, err = c.externalText(ctx, w, m.Position)
				if err != nil {
					return nil, err
				}
				if format == "sft" {
					content, _ = json.Marshal(map[string]string{"role": m.Role, "content": m.Text})
				} else {
					r := externalView(m)
					body, _ := json.Marshal(m)
					content, _ = json.Marshal(map[string]any{"request_id": r.RequestID, "session_id": id, "source_format": "external-native-event", "started_at": r.StartedAt, "completed_at": r.CompletedAt, "original_request": body, "response": []byte{}})
				}
			}
		}
	} else {
		var rows []clickhouseTurn
		selectSQL := `SELECT request_id AS turn_id,started_at AS first_at,model,outcome,status_code FROM {db:Identifier}.request_facts AS f WHERE ` + timeFilter + ` AND conversation_id={session:String} AND ` + conversationTraffic
		if format == "sft" {
			selectSQL = `SELECT * FROM (` + selectSQL + ` AND outcome='succeeded' AND status_code BETWEEN 200 AND 299 AND NOT ` + groupedSideCall + ` ORDER BY started_at DESC,request_id DESC LIMIT 1)`
		}
		if format == "archive" {
			selectSQL = numberedTurns + `SELECT argMax(request_id,(started_at,request_id)) AS turn_id,max(started_at) AS first_at,argMax(model,(started_at,request_id)) AS model,argMax(outcome,(started_at,request_id)) AS outcome,argMax(status_code,(started_at,request_id)) AS status_code FROM grouped GROUP BY turn_key HAVING turn_id IN (SELECT request_id FROM {db:Identifier}.requests WHERE ` + timeFilter + `)`
		}
		err = c.query(ctx, selectSQL+` ORDER BY first_at,turn_id LIMIT 1 OFFSET {offset:UInt32} SETTINGS max_threads=1,max_block_size=1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			endSession = true
		} else {
			t := rows[0]
			t.SessionID = id
			key.Source = t.TurnID
			key.Started = t.FirstAt.UnixNano()
			record = c.cachedExport(key)
			if record == nil {
				rw := w
				rw.params = cloneValues(w.params)
				rw.params.Set("from", t.FirstAt.Format("2006-01-02 15:04:05.999999999"))
				rw.params.Set("to", t.FirstAt.Add(time.Nanosecond).Format("2006-01-02 15:04:05.999999999"))
				rw.params.Set("id", t.TurnID)
				r, e := c.rawRequest(ctx, rw, t)
				if errors.Is(e, bodystore.ErrIncomplete) {
					ticket["skipped_incomplete"] = 1
					ticket["next_offset"] = offset + 1
					return ticket, nil
				}
				if e != nil {
					return nil, e
				}
				if format == "archive" {
					content, _ = json.Marshal(map[string]any{"request_id": r.RequestID, "session_id": id, "trace_id": r.TraceID, "model": r.Model, "requested_model": r.RequestedModel, "source_format": r.SourceFormat, "outcome": r.Outcome, "status_code": r.StatusCode, "started_at": r.StartedAt, "completed_at": r.CompletedAt, "original_request": base64.StdEncoding.EncodeToString(r.OriginalRequest), "response": base64.StdEncoding.EncodeToString(r.Response)})
				} else {
					content, err = c.trainingSnapshot(ctx, rw, r)
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if !endSession && record == nil && format == "sft" && !strings.HasPrefix(id, "external:") && len(content) == 0 {
		ticket["next_offset"] = offset + 1
		return ticket, nil
	}
	if endSession {
		if part > 0 {
			return nil, badQuery("export record disappeared while continuing; restart the export")
		}
		if format == "sft" && strings.HasPrefix(id, "external:") && offset > 0 {
			ticket["content"] = "]}\n"
		}
		ticket["next_session_offset"] = sessionIndex + 1
		ticket["next_offset"] = 0
		return ticket, nil
	}
	if record == nil && format == "sft" && strings.HasPrefix(id, "external:") {
		if offset == 0 {
			content = append([]byte(`{"messages":[`), content...)
		} else if len(content) > 0 {
			content = append([]byte(","), content...)
		}
	}
	if record == nil {
		record = newSerializedExport(string(content))
		c.rememberExport(key, record)
	}
	fragment, end, err := record.chunk(part)
	if err != nil {
		return nil, err
	}
	ticket["content"] = fragment
	if end == record.characters {
		if format == "archive" || !strings.HasPrefix(id, "external:") {
			ticket["content"] = ticket["content"].(string) + "\n"
		}
		ticket["next_offset"] = offset + 1
	} else {
		ticket["next_byte_offset"] = end
	}
	return ticket, nil
}

// Raw input retains resent history; normalized output also covers SSE responses.
func (c *clickhouseReader) trainingSnapshot(ctx context.Context, w readWindow, r Record) ([]byte, error) {
	example, _ := trainingExample(Record{OriginalRequest: r.OriginalRequest})
	complete, usable := trainingExample(r)
	if usable && len(complete.Messages) > len(example.Messages) {
		return json.Marshal(complete)
	}
	filter := ""
	if len(example.Messages) > 0 {
		filter = " AND direction='output'"
	}
	from := ` FROM (SELECT arrayStringConcat(arrayMap(x->x.3,arraySort(x->(x.1,x.2),groupArray((direction,position,message)))),',') AS fragment FROM (
SELECT direction,position,multiIf(role='tool',toJSONString(CAST(('tool',call_id,text),'Tuple(role String,tool_call_id String,content String)')),
role='assistant' AND tool_name!='',toJSONString(CAST(('assistant',[(call_id,'function',(tool_name,arguments))]),'Tuple(role String,tool_calls Array(Tuple(id String,type String,function Tuple(name String,arguments String))))')),
toJSONString(map('role',role,'content',text))) AS message
FROM {db:Identifier}.messages WHERE ` + timeFilter + ` AND request_id={id:String} AND type NOT IN ('reasoning','thinking')` + filter + `))`
	fragment, err := c.rawBytes(ctx, w, "fragment", from)
	if err != nil {
		return nil, err
	}
	var messages []map[string]any
	if err = json.Unmarshal(append(append([]byte("["), fragment...), ']'), &messages); err != nil {
		return nil, err
	}
	example.Messages = append(example.Messages, messages...)
	user, assistant := false, false
	for _, message := range example.Messages {
		user = user || message["role"] == "user"
		assistant = assistant || message["role"] == "assistant"
	}
	if !user || !assistant {
		return nil, nil
	}
	return json.Marshal(example)
}
