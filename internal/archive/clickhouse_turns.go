package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type clickhouseTurn struct {
	TurnSummary
	Reasoning       string `json:"reasoning,omitempty"`
	DurationMS      int64  `json:"duration_ms"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
	Client          string `json:"client"`
	Kind            string `json:"kind"`
	InputItems      int    `json:"input_items"`
	ToolDefinitions int    `json:"tool_definitions"`
}

const turnColumns = `f.request_id AS turn_id,conversation_id AS session_id,substringUTF8(last_user_text,1,1600) AS user_text,
 substringUTF8(reply_text,1,12000) AS final_text,substringUTF8(reasoning_text,1,12000) AS reasoning,1 AS requests,
 started_at AS first_at,addMilliseconds(started_at,duration_ms) AS last_at,key_id,model,outcome,status_code,tools_called AS tool_names,
 duration_ms,input_tokens,output_tokens,reasoning_tokens,total_tokens,client,kind,input_items,length(tools_offered) AS tool_definitions`

func (c *clickhouseReader) turns(ctx context.Context, w readWindow, q url.Values, endpoint string, limit, offset int) (any, error) {
	id := q.Get("session_id")
	if id == "" {
		return nil, badQuery("session_id is required")
	}
	w.params.Set("session", id)
	w.params.Set("turn", q.Get("turn_id"))
	if strings.HasPrefix(id, "external:") {
		return c.externalTurns(ctx, w, q, endpoint, limit, offset)
	}
	where := timeFilter + ` AND conversation_id={session:String}`
	if endpoint == "turn-text" {
		if q.Get("turn_id") == "" {
			return nil, badQuery("turn_id is required")
		}
		var rows []struct {
			Text string `json:"text"`
		}
		err := c.query(ctx, `SELECT last_user_text AS text FROM {db:Identifier}.request_facts WHERE `+where+` AND request_id={turn:String} LIMIT 1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		return map[string]string{"status": "ok", "text": rows[0].Text}, nil
	}
	if q.Get("turn_id") != "" {
		rows := []clickhouseTurn{}
		err := c.query(ctx, `SELECT `+strings.ReplaceAll(strings.ReplaceAll(turnColumns, "substringUTF8(reply_text,1,12000)", "reply_text"), "substringUTF8(reasoning_text,1,12000)", "reasoning_text")+` `+factsWithUsage+` WHERE `+where+` AND f.request_id={turn:String} LIMIT 1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		records := []RequestTimelineView{}
		if offset == 0 {
			records = append(records, turnPreview(rows[0]))
		}
		return map[string]any{"turn": rows[0], "records": records, "total": 1, "limit": limit, "offset": offset}, nil
	}
	total, err := c.count(ctx, w, `{db:Identifier}.request_facts`, where)
	if err != nil {
		return nil, err
	}
	order := "ASC"
	if q.Get("order") == "desc" {
		order = "DESC"
	}
	rows := []clickhouseTurn{}
	err = c.query(ctx, `SELECT `+turnColumns+` `+factsWithUsage+` WHERE `+where+` ORDER BY started_at `+order+`,f.request_id `+order+` LIMIT {limit:UInt32} OFFSET {offset:UInt32}`, w.params, &rows)
	return map[string]any{"turns": rows, "total": total, "limit": limit, "offset": offset}, err
}
func (c *clickhouseReader) count(ctx context.Context, w readWindow, table, where string) (int64, error) {
	var rows []struct {
		N int64 `json:"n"`
	}
	err := c.query(ctx, `SELECT count() AS n FROM `+table+` WHERE `+where+` LIMIT 1`, w.params, &rows)
	if len(rows) == 0 {
		return 0, err
	}
	return rows[0].N, err
}
func turnPreview(t clickhouseTurn) RequestTimelineView {
	view := RequestTimelineView{RequestID: t.TurnID, SessionID: t.SessionID, KeyID: t.KeyID, Summary: t.UserText, Model: t.Model, Outcome: t.Outcome, StatusCode: t.StatusCode, StartedAt: t.FirstAt, CompletedAt: t.LastAt, Kind: "turn", Entries: []TimelineEntry{}}
	view.InputItems = t.InputItems
	view.ToolDefinitions = t.ToolDefinitions
	if t.Kind == "compact" {
		view.Kind = "compaction"
	}
	if t.Reasoning != "" {
		view.Entries = append(view.Entries, TimelineEntry{Role: "assistant", Label: "reasoning", Text: t.Reasoning})
	}
	for _, tool := range t.ToolNames {
		view.Entries = append(view.Entries, TimelineEntry{Role: "tool_call", Label: tool})
	}
	return view
}

type externalMessage struct {
	SessionID       string    `json:"session_id"`
	NativeSessionID string    `json:"native_session_id"`
	Source          string    `json:"source"`
	Project         string    `json:"project"`
	Title           string    `json:"title"`
	TS              time.Time `json:"ts"`
	Position        uint32    `json:"position"`
	Role            string    `json:"role"`
	Type            string    `json:"type"`
	ToolName        string    `json:"tool_name"`
	Text            string    `json:"text"`
}

const externalColumns = `session_id,native_session_id,source,project,title,ts,position,role,type,tool_name,text`

func (c *clickhouseReader) externalTurns(ctx context.Context, w readWindow, q url.Values, endpoint string, limit, offset int) (any, error) {
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, archiveNotFound
	}
	w.params.Set("session", strings.TrimPrefix(q.Get("session_id"), "external:"))
	where := `ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} AND session_id={session:String}`
	if q.Get("turn_id") != "" {
		pos, err := strconv.ParseUint(q.Get("turn_id"), 10, 32)
		if err != nil {
			return nil, badQuery("invalid imported event position")
		}
		w.params.Set("position", strconv.FormatUint(pos, 10))
		where += ` AND position={position:UInt32}`
	}
	rows := []externalMessage{}
	order := "ASC"
	if q.Get("order") == "desc" {
		order = "DESC"
	}
	pageClause := ` LIMIT {limit:UInt32} OFFSET {offset:UInt32}`
	if q.Get("turn_id") != "" {
		pageClause = ` LIMIT 1`
	}
	err = c.query(ctx, `SELECT `+externalColumns+` FROM {db:Identifier}.external_messages FINAL WHERE `+where+` ORDER BY position `+order+pageClause, w.params, &rows)
	if err != nil {
		return nil, err
	}
	if q.Get("turn_id") != "" {
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		m := rows[0]
		if endpoint == "turn-text" {
			return map[string]string{"status": "ok", "text": m.Text}, nil
		}
		turn := externalTurn(m)
		records := []RequestTimelineView{}
		if offset == 0 {
			records = append(records, externalView(m))
		}
		return map[string]any{"turn": turn, "records": records, "total": 1, "limit": limit, "offset": offset}, nil
	}
	if endpoint == "turn-text" {
		return nil, badQuery("turn_id is required")
	}
	total, err := c.count(ctx, w, `{db:Identifier}.external_messages FINAL`, where)
	if err != nil {
		return nil, err
	}
	turns := []clickhouseTurn{}
	for _, m := range rows {
		turns = append(turns, externalTurn(m))
	}
	return map[string]any{"turns": turns, "total": total, "limit": limit, "offset": offset}, nil
}
func externalTurn(m externalMessage) clickhouseTurn {
	t := clickhouseTurn{TurnSummary: TurnSummary{TurnID: strconv.FormatUint(uint64(m.Position), 10), SessionID: "external:" + m.SessionID, Requests: 1, FirstAt: m.TS, LastAt: m.TS, Outcome: "imported"}, Client: m.Source}
	if m.Role == "user" {
		t.UserText = m.Text
	} else {
		t.UserText = m.Title
		t.FinalText = m.Text
	}
	if m.ToolName != "" {
		t.ToolNames = []string{m.ToolName}
	}
	return t
}
func externalView(m externalMessage) RequestTimelineView {
	id := "external:" + m.SessionID + ":" + strconv.FormatUint(uint64(m.Position), 10)
	return RequestTimelineView{RequestID: id, SessionID: "external:" + m.SessionID, Summary: m.Title, Outcome: "imported", StartedAt: m.TS, CompletedAt: m.TS, Kind: "turn", Facets: map[string][]string{"source.kind": {"external-local-history"}, "source.tool": {m.Source}}, Entries: []TimelineEntry{{Role: m.Role, Text: m.Text, Label: m.ToolName, Timestamp: &m.TS}}, Metadata: map[string]any{"external_session": map[string]any{"id": m.NativeSessionID, "title": m.Title, "project_path": m.Project}}}
}
func (c *clickhouseReader) request(ctx context.Context, w readWindow, q url.Values, endpoint string, limit int) (any, error) {
	id := q.Get("id")
	if id == "" {
		return nil, badQuery("id is required")
	}
	w.params.Set("id", id)
	if strings.HasPrefix(id, "external:") {
		at := strings.LastIndex(id, ":")
		if at <= len("external:") {
			return nil, badQuery("invalid imported event")
		}
		pos, err := strconv.ParseUint(id[at+1:], 10, 32)
		if err != nil {
			return nil, badQuery("invalid imported event position")
		}
		present, err := c.externalExists(ctx)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, archiveNotFound
		}
		w.params.Set("session", id[len("external:"):at])
		w.params.Set("position", strconv.FormatUint(pos, 10))
		var rows []externalMessage
		err = c.query(ctx, `SELECT `+externalColumns+` FROM {db:Identifier}.external_messages FINAL WHERE session_id={session:String} AND position={position:UInt32} AND ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} LIMIT 1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		m := rows[0]
		v := externalView(m)
		if endpoint == "request-view" {
			return v, nil
		}
		body, _ := json.Marshal(m)
		rec := Record{RequestID: id, SessionID: v.SessionID, SourceFormat: "external-native-event", Outcome: "imported", StartedAt: m.TS, CompletedAt: m.TS, OriginalRequest: body, Facets: v.Facets, Metadata: v.Metadata}
		if endpoint == "request-context" {
			return []Record{rec}, nil
		}
		return rec, nil
	}
	var facts []clickhouseTurn
	err := c.query(ctx, `SELECT `+turnColumns+` `+factsWithUsage+` WHERE `+timeFilter+` AND f.request_id={id:String} LIMIT 1`, w.params, &facts)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return nil, archiveNotFound
	}
	t := facts[0]
	if endpoint == "request-view" {
		view := turnPreview(t)
		var messages []struct {
			Role      string `json:"role"`
			Type      string `json:"type"`
			Tool      string `json:"tool_name"`
			CallID    string `json:"call_id"`
			Text      string `json:"text"`
			Arguments string `json:"arguments"`
		}
		err = c.query(ctx, `SELECT role,type,tool_name,call_id,substringUTF8(text,1,40000) AS text,substringUTF8(arguments,1,40000) AS arguments FROM {db:Identifier}.messages WHERE `+timeFilter+` AND request_id={id:String} ORDER BY direction,position LIMIT 100`, w.params, &messages)
		if err != nil {
			return nil, err
		}
		view.Entries = []TimelineEntry{}
		for _, m := range messages {
			if m.Role == "system" || m.Role == "developer" {
				view.SystemChars += len([]rune(m.Text))
			}
			role := m.Role
			text := m.Text
			if m.Tool != "" && role == "assistant" {
				role = "tool_call"
				text = m.Arguments
			}
			if role == "tool" {
				role = "tool_result"
			}
			label := m.Tool
			if m.Type == "reasoning" || m.Type == "thinking" {
				label = "reasoning"
			}
			view.Entries = append(view.Entries, TimelineEntry{Role: role, Label: label, CallID: m.CallID, Text: text})
		}
		return view, nil
	}
	if endpoint == "request-context" {
		w.params.Set("session", t.SessionID)
		w.params.Set("start", t.FirstAt.Format("2006-01-02 15:04:05.999999999"))
		w.params.Set("limit", strconv.Itoa(min(limit, 32)))
		var messages []struct {
			CallID string `json:"call_id"`
			Text   string `json:"text"`
		}
		err = c.query(ctx, `SELECT call_id,substringUTF8(text,1,200000) AS text FROM {db:Identifier}.messages WHERE `+timeFilter+` AND conversation_id={session:String} AND started_at >= {start:DateTime64(9,'UTC')} AND role='tool' ORDER BY started_at,position LIMIT {limit:UInt32}`, w.params, &messages)
		if err != nil {
			return nil, err
		}
		output := []map[string]string{}
		for _, m := range messages {
			output = append(output, map[string]string{"type": "function_call_output", "call_id": m.CallID, "output": m.Text})
		}
		body, _ := json.Marshal(map[string]any{"output": output})
		return []Record{{RequestID: id, SessionID: t.SessionID, Response: body}}, nil
	}
	var rows []struct {
		RequestID      string    `json:"request_id"`
		TraceID        string    `json:"trace_id"`
		SourceFormat   string    `json:"source_format"`
		RequestedModel string    `json:"requested_model"`
		Stream         bool      `json:"stream"`
		Error          string    `json:"error"`
		StartedAt      time.Time `json:"started_at"`
		CompletedAt    time.Time `json:"completed_at"`
		RequestBody    string    `json:"request_body"`
		ResponseBody   string    `json:"response_body"`
		ResponseChunks []string  `json:"response_chunks"`
	}
	err = c.query(ctx, `SELECT request_id,trace_id,source_format,requested_model,stream,error,started_at,completed_at,request_body,response_body,response_chunks FROM {db:Identifier}.requests WHERE `+timeFilter+` AND request_id={id:String} LIMIT 1`, w.params, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, archiveNotFound
	}
	r := rows[0]
	response := r.ResponseBody
	if response == "" {
		response = strings.Join(r.ResponseChunks, "")
	}
	return Record{RequestID: id, TraceID: r.TraceID, SessionID: t.SessionID, KeyID: t.KeyID, Summary: t.UserText, Model: t.Model, RequestedModel: r.RequestedModel, SourceFormat: r.SourceFormat, Stream: r.Stream, Outcome: t.Outcome, StatusCode: t.StatusCode, Error: r.Error, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, OriginalRequest: []byte(r.RequestBody), Response: []byte(response)}, nil
}
