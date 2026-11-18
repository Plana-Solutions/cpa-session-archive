package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
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

const conversationTraffic = `kind NOT IN ('count_tokens','models','embeddings')`
const tailHasTool = `arrayExists((d,r)->d='input' AND r='tool',msg_direction,msg_role)`
const tailHasUser = `arrayExists((d,r,t)->d='input' AND r='user' AND t='message',msg_direction,msg_role,msg_type)`
const turnStart = `(NOT prior_seen OR (NOT ` + tailHasTool + ` AND ` + tailHasUser + `))`
const turnUserText = `arrayFirst((text,d,r,t)->d='input' AND r='user' AND t='message',msg_text,msg_direction,msg_role,msg_type)`

// Keep turn identity shared by lists, detail and exports. A tool-result tail continues a Claude turn even when it includes user text.
const numberedTurns = `WITH steps AS (
 SELECT *,lagInFrame(turn_id) OVER w AS prev_codex,row_number() OVER w AS rn
 FROM {db:Identifier}.request_facts WHERE ` + timeFilter + ` AND conversation_id={session:String} AND ` + conversationTraffic + `
 WINDOW w AS (ORDER BY started_at,request_id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
), numbered AS (
 SELECT *,sum(rn=1 OR if(turn_id!='',turn_id!=prev_codex,` + turnStart + `)) OVER (ORDER BY started_at,request_id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS turn_no FROM steps
), grouped AS (
 SELECT *,if(turn_id!='',turn_id,first_value(request_id) OVER (PARTITION BY turn_no ORDER BY started_at,request_id)) AS turn_key FROM numbered
) `
const groupedTurnColumns = `turn_key AS turn_id,any(conversation_id) AS session_id,
 substringUTF8(argMin(` + turnUserText + `,(started_at,request_id)),1,1600) AS user_text,
 substringUTF8(argMaxIf(reply_text,(started_at,request_id),reply_text!=''),1,12000) AS final_text,
 count() AS requests,min(started_at) AS first_at,max(addMilliseconds(started_at,f.duration_ms)) AS last_at,
 argMax(key_id,(started_at,request_id)) AS key_id,argMax(f.model,(started_at,request_id)) AS model,
 argMax(f.outcome,(started_at,request_id)) AS outcome,argMax(f.status_code,(started_at,request_id)) AS status_code,
 arraySort(groupUniqArrayArray(tools_called)) AS tool_names,
 sum(f.duration_ms) AS duration_ms,sum(u.input_tokens) AS input_tokens,sum(u.output_tokens) AS output_tokens,
 sum(u.reasoning_tokens) AS reasoning_tokens,sum(u.total_tokens) AS total_tokens`

func (c *clickhouseReader) narrow(ctx context.Context, w readWindow, where string) (readWindow, error) {
	var rows []struct{ Lo, Hi string }
	err := c.query(ctx, `SELECT toString(min(started_at)) AS Lo,toString(addNanoseconds(max(started_at),1)) AS Hi FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+where+` HAVING count()>0 LIMIT 1`, w.params, &rows)
	if err == nil && len(rows) > 0 {
		w.params.Set("from", rows[0].Lo)
		w.params.Set("to", rows[0].Hi)
	}
	return w, err
}
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
	var err error
	w, err = c.narrow(ctx, w, `conversation_id={session:String} AND `+conversationTraffic)
	if err != nil {
		return nil, err
	}
	if endpoint == "turn-text" {
		if q.Get("turn_id") == "" {
			return nil, badQuery("turn_id is required")
		}
		var rows []struct {
			Text string `json:"text"`
		}
		err = c.query(ctx, numberedTurns+`SELECT `+turnUserText+` AS text FROM grouped WHERE turn_key={turn:String} ORDER BY started_at,request_id LIMIT 1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		return map[string]string{"status": "ok", "text": rows[0].Text}, nil
	}
	join := strings.Replace(factsWithUsage, "{db:Identifier}.request_facts AS f", "grouped AS f", 1)
	filter := ""
	if q.Get("turn_id") != "" {
		filter = ` WHERE turn_key={turn:String}`
	}
	rows := []clickhouseTurn{}
	order := "ASC"
	if q.Get("order") == "desc" {
		order = "DESC"
	}
	page := ` ORDER BY first_at ` + order + `,turn_id ` + order + ` LIMIT {limit:UInt32} OFFSET {offset:UInt32}`
	if filter != "" {
		page = ` LIMIT 1`
	}
	err = c.query(ctx, numberedTurns+`SELECT `+groupedTurnColumns+` `+join+filter+` GROUP BY turn_key`+page, w.params, &rows)
	if err != nil {
		return nil, err
	}
	if filter != "" {
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		steps := []clickhouseTurn{}
		err = c.query(ctx, numberedTurns+`SELECT `+turnColumns+` `+join+filter+` ORDER BY started_at,f.request_id LIMIT {limit:UInt32} OFFSET {offset:UInt32}`, w.params, &steps)
		records := []RequestTimelineView{}
		for _, step := range steps {
			records = append(records, turnPreview(step))
		}
		return map[string]any{"turn": rows[0], "records": records, "total": rows[0].Requests, "limit": limit, "offset": offset}, err
	}
	var totals []struct {
		N int64 `json:"n"`
	}
	err = c.query(ctx, numberedTurns+`SELECT uniqExact(turn_key) AS n FROM grouped LIMIT 1`, w.params, &totals)
	total := int64(0)
	if len(totals) > 0 {
		total = totals[0].N
	}
	return map[string]any{"turns": rows, "total": total, "limit": limit, "offset": offset}, err
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

const externalGroups = `WITH events AS (SELECT *,sum(role='user' OR position=first_position) OVER (ORDER BY position ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS turn_no FROM (
 SELECT *,min(position) OVER () AS first_position FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} AND session_id={session:String}
)), grouped AS (SELECT *,min(position) OVER (PARTITION BY turn_no) AS turn_position FROM events) `

func (c *clickhouseReader) externalTurns(ctx context.Context, w readWindow, q url.Values, endpoint string, limit, offset int) (any, error) {
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, archiveNotFound
	}
	w.params.Set("session", strings.TrimPrefix(q.Get("session_id"), "external:"))
	filter := ""
	if q.Get("turn_id") != "" {
		pos, e := strconv.ParseUint(q.Get("turn_id"), 10, 32)
		if e != nil {
			return nil, badQuery("invalid imported turn position")
		}
		w.params.Set("position", strconv.FormatUint(pos, 10))
		filter = ` WHERE turn_position={position:UInt32}`
	}
	if endpoint == "turn-text" {
		if filter == "" {
			return nil, badQuery("turn_id is required")
		}
		var rows []struct {
			Text string `json:"text"`
		}
		err = c.query(ctx, externalGroups+`SELECT text FROM grouped`+filter+` ORDER BY position LIMIT 1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, archiveNotFound
		}
		return map[string]string{"status": "ok", "text": rows[0].Text}, nil
	}
	order := "ASC"
	if q.Get("order") == "desc" {
		order = "DESC"
	}
	page := ` ORDER BY turn_position ` + order + ` LIMIT {limit:UInt32} OFFSET {offset:UInt32}`
	if filter != "" {
		page = ` LIMIT 1`
	}
	turns := []clickhouseTurn{}
	err = c.query(ctx, externalGroups+`SELECT toString(turn_position) AS turn_id,concat('external:',any(session_id)) AS session_id,substring(argMin(text,position),1,1600) AS user_text,substring(argMaxIf(text,position,role='assistant' AND text!=''),1,12000) AS final_text,count() AS requests,min(ts) AS first_at,max(ts) AS last_at,'imported' AS outcome,any(source) AS client,groupUniqArrayIf(tool_name,tool_name!='') AS tool_names FROM grouped`+filter+` GROUP BY turn_position`+page, w.params, &turns)
	if err != nil {
		return nil, err
	}
	if filter != "" {
		if len(turns) == 0 {
			return nil, archiveNotFound
		}
		rows := []externalMessage{}
		err = c.query(ctx, externalGroups+`SELECT `+externalColumns+` FROM grouped`+filter+` ORDER BY position LIMIT {limit:UInt32} OFFSET {offset:UInt32}`, w.params, &rows)
		records := []RequestTimelineView{}
		for _, m := range rows {
			records = append(records, externalView(m))
		}
		return map[string]any{"turn": turns[0], "records": records, "total": turns[0].Requests, "limit": limit, "offset": offset}, err
	}
	var counts []struct {
		N int64 `json:"n"`
	}
	err = c.query(ctx, externalGroups+`SELECT uniqExact(turn_position) AS n FROM grouped LIMIT 1`, w.params, &counts)
	total := int64(0)
	if len(counts) > 0 {
		total = counts[0].N
	}
	return map[string]any{"turns": turns, "total": total, "limit": limit, "offset": offset}, err
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
	w, err := c.narrow(ctx, w, `request_id={id:String}`)
	if err != nil {
		return nil, err
	}
	err = c.query(ctx, `SELECT `+turnColumns+` `+factsWithUsage+` WHERE `+timeFilter+` AND f.request_id={id:String} LIMIT 1`, w.params, &facts)
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
			Chars     int    `json:"chars"`
			Direction string `json:"direction"`
			Position  uint32 `json:"position"`
			Role      string `json:"role"`
			Type      string `json:"type"`
			Tool      string `json:"tool_name"`
			CallID    string `json:"call_id"`
			Text      string `json:"text"`
			Arguments string `json:"arguments"`
		}
		err = c.query(ctx, `SELECT lengthUTF8(m.text) AS chars,direction,position,role,type,tool_name,call_id,substring(text,1,8000) AS text,substring(arguments,1,8000) AS arguments FROM {db:Identifier}.messages AS m WHERE `+timeFilter+` AND request_id={id:String} ORDER BY direction,position DESC LIMIT 80 BY direction LIMIT 160`, w.params, &messages)
		if err != nil {
			return nil, err
		}
		sort.Slice(messages, func(i, j int) bool {
			if messages[i].Direction != messages[j].Direction {
				return messages[i].Direction < messages[j].Direction
			}
			return messages[i].Position < messages[j].Position
		})
		view.Entries = []TimelineEntry{}
		for _, m := range messages {
			if m.Role == "system" || m.Role == "developer" {
				view.SystemChars += m.Chars
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
		if q.Get("call_id") == "" {
			return nil, badQuery("call_id is required")
		}
		w.params.Set("call", q.Get("call_id"))
		w.params.Set("start", t.FirstAt.Format("2006-01-02 15:04:05.999999999"))
		w.params.Set("limit", strconv.Itoa(min(limit, 32)))
		var messages []struct {
			CallID string `json:"call_id"`
			Text   string `json:"text"`
		}
		err = c.query(ctx, `SELECT call_id,substring(text,1,64000) AS text FROM {db:Identifier}.messages WHERE conversation_id={session:String} AND started_at > {start:DateTime64(9,'UTC')} AND started_at < {start:DateTime64(9,'UTC')} + INTERVAL 1 DAY AND call_id={call:String} AND role='tool' ORDER BY started_at,position LIMIT {limit:UInt32}`, w.params, &messages)
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
