package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const resultLimit = 8 << 20

type archiveReadError struct {
	status  int
	message string
}

func (e *archiveReadError) Error() string { return e.message }
func badQuery(message string) error       { return &archiveReadError{400, message} }

var archiveNotFound = &archiveReadError{404, "not found"}

type clickhouseReader struct {
	config          pluginConfig
	client          *http.Client
	searchesMu      sync.Mutex
	searches        map[string]*activeSearch
	externalMu      sync.Mutex
	externalChecked time.Time
	externalPresent bool
	slots           chan struct{}
	exportMu        sync.Mutex
	exportCache     *exportCacheEntry
}
type activeSearch struct{ cancel context.CancelFunc }

type readWindow struct {
	from, to time.Time
	params   url.Values
}

func windowFor(q url.Values, defaultDays int) (readWindow, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	w := readWindow{from: today.AddDate(0, 0, 1-defaultDays), to: now}
	if q.Get("days") != "" {
		days, err := strconv.Atoi(q.Get("days"))
		if err != nil || days < 1 || days > 366 {
			return w, badQuery("days must be between 1 and 366")
		}
		w.from = today.AddDate(0, 0, 1-days)
	}
	for _, name := range []string{"from", "to"} {
		if value := q.Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				parsed, err = time.Parse(time.DateOnly, value)
				if name == "to" {
					parsed = parsed.AddDate(0, 0, 1)
				}
			}
			if err != nil {
				return w, badQuery("dates must be UTC dates or RFC3339 timestamps")
			}
			if name == "from" {
				w.from = parsed.UTC()
			} else {
				w.to = parsed.UTC()
			}
		}
	}
	if !w.from.Before(w.to) || w.to.Sub(w.from) > 366*24*time.Hour {
		return w, badQuery("date range must be positive and at most 366 days")
	}
	w.params = url.Values{"from": {w.from.Format("2006-01-02 15:04:05.999999999")}, "to": {w.to.Format("2006-01-02 15:04:05.999999999")}}
	return w, nil
}

const timeFilter = `started_at >= {from:DateTime64(9,'UTC')} AND started_at < {to:DateTime64(9,'UTC')}`

func (c *clickhouseReader) query(ctx context.Context, sql string, params url.Values, out any) error {
	u, err := url.Parse(c.config.URL)
	if err != nil {
		return errors.New("invalid ClickHouse endpoint")
	}
	q := u.Query()
	q.Set("param_db", c.config.Database)
	for k, v := range params {
		if len(v) > 0 {
			q.Set("param_"+k, strings.ReplaceAll(v[0], `\`, `\\`))
		}
	}
	q.Set("default_format", "JSONEachRow")
	q.Set("output_format_json_quote_64bit_integers", "0")
	q.Set("date_time_output_format", "iso")
	q.Set("max_execution_time", "55")
	if params.Get("search") != "" {
		q.Set("max_execution_time", "3")
	}
	q.Set("max_result_bytes", strconv.Itoa(resultLimit))
	q.Set("result_overflow_mode", "throw")
	q.Set("wait_end_of_query", "1")
	q.Set("cancel_http_readonly_queries_on_client_close", "1")
	q.Set("buffer_size", strconv.Itoa(resultLimit))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(sql))
	if err != nil {
		return errors.New("invalid ClickHouse request")
	}
	req.SetBasicAuth(c.config.User, c.config.Password)
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("ClickHouse read unavailable or timed out")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, resultLimit+1))
	if err != nil {
		return errors.New("ClickHouse response interrupted")
	}
	if len(body) > resultLimit {
		return errors.New("ClickHouse result exceeds 8 MiB; narrow the range")
	}
	if resp.StatusCode != 200 {
		if params.Get("search") != "" && (resp.Header.Get("X-ClickHouse-Exception-Code") == "159" || strings.Contains(string(body), "TIMEOUT_EXCEEDED")) {
			return &archiveReadError{504, "search timed out, narrow the range"}
		}
		return fmt.Errorf("ClickHouse read failed (HTTP %d); check schema, reader access and result limits", resp.StatusCode)
	}
	// Decode only after the whole bounded response succeeds, so partial rows never appear as a complete page.
	rows := []json.RawMessage{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	for {
		var row json.RawMessage
		err := dec.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid ClickHouse response")
		}
		rows = append(rows, row)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func boundedInt(q url.Values, name string, fallback, maximum int) (int, error) {
	if q.Get(name) == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(q.Get(name))
	minimum := 1
	if name == "offset" || strings.HasSuffix(name, "_offset") {
		minimum = 0
	}
	if err != nil || v < minimum || v > maximum {
		return 0, badQuery(name + " is outside the supported range")
	}
	return v, nil
}
func (c *clickhouseReader) externalExists(ctx context.Context) (bool, error) {
	c.externalMu.Lock()
	defer c.externalMu.Unlock()
	if time.Since(c.externalChecked) < 30*time.Second {
		return c.externalPresent, nil
	}
	var present []struct {
		Present uint8 `json:"present"`
	}
	err := c.query(ctx, `SELECT count() AS present FROM system.tables WHERE database={database:String} AND name='external_messages' LIMIT 1`, url.Values{"database": {c.config.Database}}, &present)
	if err == nil {
		c.externalChecked = time.Now()
		c.externalPresent = len(present) > 0 && present[0].Present > 0
	}
	return c.externalPresent, err
}
func (c *clickhouseReader) read(endpoint string, q url.Values) (any, error) {
	return c.readContext(context.Background(), endpoint, q)
}
func (c *clickhouseReader) readContext(parent context.Context, endpoint string, q url.Values) (out any, readErr error) {
	if endpoint == "cancel-search" {
		c.searchesMu.Lock()
		if search := c.searches[q.Get("search_client")]; search != nil {
			search.cancel()
		}
		c.searchesMu.Unlock()
		return map[string]string{"status": "ok"}, nil
	}
	timeout := 60 * time.Second
	search := endpoint == "sessions" && q.Get("q") != ""
	if search {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer func() {
		if search && (errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(readErr, context.DeadlineExceeded)) {
			out = nil
			readErr = &archiveReadError{504, "search timed out, narrow the range"}
		}
	}()
	defer cancel()
	if endpoint == "sessions" && q.Get("search_client") != "" {
		c.searchesMu.Lock()
		if c.searches == nil {
			c.searches = map[string]*activeSearch{}
		}
		key := q.Get("search_client")
		if old := c.searches[key]; old != nil {
			old.cancel()
		}
		search := &activeSearch{cancel: cancel}
		c.searches[key] = search
		c.searchesMu.Unlock()
		defer func() {
			c.searchesMu.Lock()
			if c.searches[key] == search {
				delete(c.searches, key)
			}
			c.searchesMu.Unlock()
		}()
	}
	c.searchesMu.Lock()
	if c.slots == nil {
		c.slots = make(chan struct{}, 4)
	}
	slots := c.slots
	c.searchesMu.Unlock()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	days := 365
	if endpoint == "insights" {
		days = 30
	} else if search {
		days = 7
	}
	w, err := windowFor(q, days)
	if err != nil {
		return nil, err
	}
	limit, err := boundedInt(q, "limit", 20, 1000)
	if err != nil {
		return nil, err
	}
	offset, err := boundedInt(q, "offset", 0, 1000000)
	if err != nil {
		return nil, err
	}
	w.params.Set("limit", strconv.Itoa(limit))
	w.params.Set("offset", strconv.Itoa(offset))
	switch endpoint {
	case "stats":
		return c.stats(ctx, w)
	case "insights":
		return c.insights(ctx, w, q)
	case "facets":
		return c.facets(ctx, w)
	case "sessions":
		return c.sessions(ctx, w, q)
	case "turns", "turn-text":
		return c.turns(ctx, w, q, endpoint, limit, offset)
	case "requests", "request-view", "request-context":
		return c.request(ctx, w, q, endpoint, limit)
	case "external-sources":
		return []any{}, nil
	case "export":
		return c.export(ctx, w, q)
	default:
		return nil, archiveNotFound
	}
}

const factsWithUsage = `FROM {db:Identifier}.request_facts AS f LEFT JOIN
 (SELECT r.request_id AS lifecycle_request_id,argMax(u.api_key_hash,u.requested_at) AS key_id,
 sum(u.input_tokens) AS input_tokens,sum(u.output_tokens) AS output_tokens,sum(u.reasoning_tokens) AS reasoning_tokens,sum(u.total_tokens) AS total_tokens
 FROM {db:Identifier}.usage AS u ASOF LEFT JOIN
 (SELECT request_id,trace_id,started_at FROM {db:Identifier}.requests WHERE trace_id!='' AND started_at >= {from:DateTime64(9,'UTC')} - INTERVAL 1 HOUR AND started_at < {to:DateTime64(9,'UTC')} + INTERVAL 1 HOUR) AS r
 ON u.trace_id=r.trace_id AND u.requested_at>=r.started_at
 WHERE u.requested_at >= {from:DateTime64(9,'UTC')} AND u.requested_at < {to:DateTime64(9,'UTC')} + INTERVAL 1 HOUR
 GROUP BY lifecycle_request_id) AS u ON f.request_id=u.lifecycle_request_id`

const systemSession = `(kind='compact' OR (request_kind!='' AND request_kind!='turn') OR thread_source='system' OR endsWith(conversation_id,':compaction') OR arrayExists((x,d,r,t)->d='input' AND r='user' AND t='message' AND (startsWith(x,'Generate a title that will help the user recognize this T3 Code thread') OR startsWith(x,'You generate concise git branch names.') OR startsWith(x,'You are performing a CONTEXT CHECKPOINT COMPACTION.')),msg_text,msg_direction,msg_role,msg_type))`

type archiveSession struct {
	System       bool      `json:"system,omitempty"`
	SessionID    string    `json:"session_id"`
	Continued    bool      `json:"continued,omitempty"`
	Summary      string    `json:"summary"`
	Project      string    `json:"project"`
	KeyID        string    `json:"key_id"`
	Model        string    `json:"model"`
	Client       string    `json:"client"`
	Requests     int64     `json:"requests"`
	FirstAt      time.Time `json:"first_at"`
	LastAt       time.Time `json:"last_at"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
	SourceKind   string    `json:"source_kind,omitempty"`
	SourceTool   string    `json:"source_tool,omitempty"`
}

func (c *clickhouseReader) sessions(ctx context.Context, w readWindow, q url.Values) ([]archiveSession, error) {
	// Filter conversation IDs, then aggregate all their requests in the window, preserving the conversation title and totals.
	filters := `conversation_id != '' AND ` + timeFilter + ` AND ` + conversationTraffic
	unfiltered := filters
	fields := map[string]string{"model.resolved": "model", "model.requested": "requested_model", "client": "client", "outcome": "outcome", "key.id": "key_id", "tool.name": "tools_called", "request.kind": "request_kind", "thread.source": "thread_source"}
	for name, field := range fields {
		if value := q.Get(name); value != "" {
			parameter := strings.ReplaceAll(name, ".", "_")
			w.params.Set(parameter, value)
			if field == "tools_called" {
				filters += ` AND has(tools_called,{` + parameter + `:String})`
			} else {
				filters += ` AND ` + field + `={` + parameter + `:String}`
			}
		}
	}
	if q.Get("project.name") != "" {
		filters += ` AND 0`
	}

	source := `FROM {db:Identifier}.request_facts AS f`
	if q.Get("key.id") != "" {
		source = factsWithUsage
	}
	if q.Get("model.requested") != "" {
		filters += ` AND request_id IN (SELECT request_id FROM {db:Identifier}.requests WHERE ` + timeFilter + ` AND requested_model={model_requested:String} LIMIT 1000000)`
		filters = strings.Replace(filters, ` AND requested_model={model_requested:String}`, "", 1)
	}
	if q.Get("q") != "" {
		w.params.Set("search", strings.Join(strings.Fields(q.Get("q")), " "))
		ids, err := c.searchSessions(ctx, w, source, filters, pageMatches(w.params))
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			filters += " AND 0"
		} else {
			encoded, _ := json.Marshal(ids)
			w.params.Set("match_ids", string(encoded))
			filters += " AND conversation_id IN (SELECT arrayJoin(JSONExtract({match_ids:String},'Array(String)')))"
		}
	}

	selection := ""
	if filters != unfiltered {
		selection = ` AND conversation_id IN (SELECT conversation_id ` + source + ` WHERE ` + filters + ` GROUP BY conversation_id)`
	}
	having := ""
	if q.Get("show_system") != "1" {
		having = ` HAVING NOT system`
	}
	sql := `SELECT CAST(max(` + systemSession + `),'Bool') AS system,conversation_id AS session_id,'' AS summary,'' AS project,'' AS key_id,argMax(model,started_at) AS model,argMax(client,started_at) AS client,count() AS requests,min(started_at) AS first_at,max(started_at) AS last_at,toInt64(0) AS input_tokens,toInt64(0) AS output_tokens,toInt64(0) AS total_tokens,'' AS source_kind,'' AS source_tool FROM {db:Identifier}.request_facts WHERE conversation_id!='' AND ` + timeFilter + ` AND ` + conversationTraffic + selection + ` GROUP BY conversation_id` + having
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if present {
		ef := `ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')}`
		for name, field := range map[string]string{"client": "source", "project.name": "project", "tool.name": "tool_name"} {
			if v := q.Get(name); v != "" {
				w.params.Set("ext_"+field, v)
				ef += ` AND ` + field + `={ext_` + field + `:String}`
			}
		}
		for _, name := range []string{"model.resolved", "model.requested", "key.id", "request.kind", "thread.source"} {
			if q.Get(name) != "" {
				ef += ` AND 0`
			}
		}
		if q.Get("outcome") != "" && q.Get("outcome") != "imported" {
			ef += ` AND 0`
		}
		if q.Get("q") != "" {
			ef += ` AND ((role='user' AND positionCaseInsensitiveUTF8(replaceRegexpAll(text,` + searchWhitespacePattern + `,' '),{search:String})>0) OR positionCaseInsensitiveUTF8(replaceRegexpAll(title,` + searchWhitespacePattern + `,' '),{search:String})>0 OR positionCaseInsensitiveUTF8(session_id,{search:String})>0 OR positionCaseInsensitiveUTF8(project,{search:String})>0 OR positionCaseInsensitiveUTF8(source,{search:String})>0)`
		}
		sql += ` UNION ALL SELECT false,concat('external:',session_id),substringUTF8(argMin(title,position),1,200),any(project),'','',any(source),count(),min(ts),max(ts),toInt64(0),toInt64(0),toInt64(0),'external-local-history',any(source) FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} AND session_id IN (SELECT session_id FROM {db:Identifier}.external_messages FINAL WHERE ` + ef + `) GROUP BY session_id`
	}
	sql = `SELECT system,session_id,summary,project,key_id,model,client,requests,first_at,last_at,input_tokens,output_tokens,total_tokens,source_kind,source_tool FROM (` + sql + `) ORDER BY last_at DESC,session_id LIMIT {limit:UInt32} OFFSET {offset:UInt32}`
	rows := []archiveSession{}
	err = c.query(ctx, sql, w.params, &rows)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for i, row := range rows {
		if strings.HasPrefix(row.SessionID, "external:") {
			continue
		}
		key := "page_" + strconv.Itoa(i)
		w.params.Set(key, row.SessionID)
		ids = append(ids, "{"+key+":String}")
	}
	if len(ids) == 0 {
		return rows, nil
	}
	where := `conversation_id IN (` + strings.Join(ids, ",") + `) AND ` + conversationTraffic
	lo, hi := w.to, w.from
	for _, row := range rows {
		if strings.HasPrefix(row.SessionID, "external:") {
			continue
		}
		if row.FirstAt.Before(lo) {
			lo = row.FirstAt
		}
		if row.LastAt.After(hi) {
			hi = row.LastAt
		}
	}
	w.params.Set("from", lo.Format("2006-01-02 15:04:05.999999999"))
	w.params.Set("to", hi.Add(time.Nanosecond).Format("2006-01-02 15:04:05.999999999"))
	var summaries []struct {
		archiveSession
		TitleRequest string `json:"title_request"`
	}
	err = c.query(ctx, `SELECT conversation_id AS session_id,argMin(f.request_id,(started_at,f.request_id)) AS title_request,argMax(key_id,started_at) AS key_id,sum(input_tokens) AS input_tokens,sum(output_tokens) AS output_tokens,sum(total_tokens) AS total_tokens `+factsWithUsage+` WHERE `+timeFilter+` AND `+where+` GROUP BY conversation_id LIMIT 1000`, w.params, &summaries)
	if err != nil {
		return nil, err
	}
	titleIDs := []string{}
	for _, summary := range summaries {
		titleIDs = append(titleIDs, summary.TitleRequest)
	}
	texts, err := c.displayTexts(ctx, w, titleIDs)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		for _, summary := range summaries {
			if rows[i].SessionID == summary.SessionID {
				text, continued := firstUserText(texts[summary.TitleRequest].Texts)
				rows[i].Summary = compactSummary(text)
				rows[i].Continued = continued
				rows[i].KeyID = summary.KeyID
				rows[i].InputTokens = summary.InputTokens
				rows[i].OutputTokens = summary.OutputTokens
				rows[i].TotalTokens = summary.TotalTokens
			}
		}
	}
	return rows, err
}
func (c *clickhouseReader) stats(ctx context.Context, w readWindow) (any, error) {
	var rows []Stats
	err := c.query(ctx, `SELECT count() AS records,uniqExactIf(conversation_id,conversation_id!='') AS sessions FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+` LIMIT 1`, w.params, &rows)
	if err != nil {
		return nil, err
	}
	out := Stats{}
	if len(rows) > 0 {
		out = rows[0]
	}
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if present {
		var ext []Stats
		err = c.query(ctx, `SELECT count() AS records,uniqExact(session_id) AS sessions FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')} LIMIT 1`, w.params, &ext)
		if err != nil {
			return nil, err
		}
		if len(ext) > 0 {
			out.Records += ext[0].Records
			out.Sessions += ext[0].Sessions
		}
	}
	return out, nil
}
func (c *clickhouseReader) facets(ctx context.Context, w readWindow) (any, error) {
	sql := `SELECT name,value,uniqExact(conversation_id) AS sessions,count() AS records FROM (
 SELECT conversation_id,arrayJoin([('model.resolved',toString(model)),('model.requested',requested_model),('request.kind',toString(request_kind)),('thread.source',toString(thread_source)),('client',toString(client)),('outcome',toString(outcome)),('key.id',key_id)] || arrayMap(t->('tool.name',toString(t)),tools_called)) AS facet,facet.1 AS name,facet.2 AS value ` + factsWithUsage + ` LEFT JOIN (SELECT request_id,requested_model FROM {db:Identifier}.requests WHERE ` + timeFilter + `) AS r ON f.request_id=r.request_id WHERE ` + timeFilter + ` AND ` + conversationTraffic + ` AND conversation_id!='') WHERE value!='' GROUP BY name,value`
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if present {
		sql += ` UNION ALL SELECT facet.1,facet.2,uniqExact(session_id),count() FROM (SELECT session_id,arrayJoin([('client',toString(source)),('project.name',project),('outcome','imported'),('tool.name',tool_name)]) AS facet FROM {db:Identifier}.external_messages FINAL WHERE ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')}) WHERE facet.2!='' GROUP BY facet`
	}
	sql = `SELECT name,value,sum(sessions) AS sessions,sum(records) AS records FROM (` + sql + `) GROUP BY name,value ORDER BY records DESC,name,value LIMIT 500`
	rows := []FacetCount{}
	err = c.query(ctx, sql, w.params, &rows)
	return rows, err
}
func (c *clickhouseReader) insights(ctx context.Context, w readWindow, q url.Values) (Insights, error) {
	out := Insights{From: w.from.Format(time.RFC3339Nano), To: w.to.Format(time.RFC3339Nano), GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), WindowDays: int(w.to.Sub(w.from).Hours()/24) + 1, Models: []ModelInsight{}, Daily: []DailyInsight{}}
	var totals []InsightTotals
	err := c.query(ctx, `SELECT count() AS requests,uniqExactIf(conversation_id,conversation_id!='') AS sessions,uniqExactIf(tuple(conversation_id,turn_id),turn_id!='' AND conversation_id!='' AND NOT `+sideCall+`) + countIf(turn_id='' AND conversation_id!='' AND `+turnStart+`) AS turns,uniqExact(model) AS models FROM (SELECT *,row_number() OVER (PARTITION BY conversation_id ORDER BY started_at,request_id) AS rn FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+`) LIMIT 1`, w.params, &totals)
	if err != nil {
		return out, err
	}
	if len(totals) > 0 {
		out.Totals = totals[0]
	}
	minimum, err := boundedInt(q, "min_requests", 1, 1000000)
	if err != nil {
		return out, err
	}
	w.params.Set("minimum", strconv.Itoa(minimum))
	err = c.query(ctx, `SELECT model,count() AS requests,uniqExactIf(conversation_id,conversation_id!='') AS sessions,uniqExactIf(tuple(conversation_id,turn_id),turn_id!='' AND conversation_id!='' AND NOT `+sideCall+`) + countIf(turn_id='' AND conversation_id!='' AND `+turnStart+`) AS turns,if(turns>0,countIf(conversation_id!='')/turns,0) AS steps_per_turn,
 ifNotFinite(avgIf(duration_ms/1000,outcome='succeeded'),0) AS avg_latency_seconds,countIf(outcome IN ('failed','rejected'))/count() AS failure_rate,countIf(outcome='canceled')/count() AS cancel_rate
 FROM (SELECT *,row_number() OVER (PARTITION BY conversation_id ORDER BY started_at,request_id) AS rn FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+`) GROUP BY model HAVING requests >= {minimum:UInt32} ORDER BY requests DESC,model LIMIT 200`, w.params, &out.Models)
	if err != nil {
		return out, err
	}
	for i := range out.Models {
		out.Models[i].Provider = modelProvider(out.Models[i].Model)
	}
	var daily []struct {
		Day      string   `json:"day"`
		Provider Provider `json:"provider"`
		Requests int64    `json:"requests"`
	}
	err = c.query(ctx, `SELECT toString(toDate(started_at,'UTC')) AS day,
 multiIf(startsWith(model,'claude-'),'anthropic',startsWith(model,'gpt-') OR startsWith(model,'o1') OR startsWith(model,'o3') OR startsWith(model,'o4') OR startsWith(model,'codex-'),'openai',startsWith(model,'gemini-'),'google','other') AS provider,
 count() AS requests FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+` GROUP BY day,provider ORDER BY day,provider LIMIT 1464`, w.params, &daily)
	if err != nil {
		return out, err
	}
	byDay := map[string]map[Provider]int64{}
	for _, row := range daily {
		if byDay[row.Day] == nil {
			byDay[row.Day] = map[Provider]int64{}
		}
		byDay[row.Day][row.Provider] += row.Requests
	}
	day := time.Date(w.from.Year(), w.from.Month(), w.from.Day(), 0, 0, 0, 0, time.UTC)
	for day.Before(w.to) {
		key := day.Format(time.DateOnly)
		counts := byDay[key]
		if counts == nil {
			counts = map[Provider]int64{}
		}
		out.Daily = append(out.Daily, DailyInsight{Day: key, Requests: counts})
		day = day.AddDate(0, 0, 1)
	}
	out.WindowDays = len(out.Daily)
	return out, nil
}

func pageMatches(params url.Values) int {
	limit, _ := strconv.Atoi(params.Get("limit"))
	offset, _ := strconv.Atoi(params.Get("offset"))
	return limit + offset
}
