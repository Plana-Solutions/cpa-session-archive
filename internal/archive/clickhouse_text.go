package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The deriver emits separate user messages for injected context and the user's prompt.
func firstUserText(texts []string) (string, bool) {
	continued := false
	first := ""
	commandFallback := false
	for _, text := range texts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "this session is being continued from a previous conversation") {
			continued = true
			continue
		}
		candidate := meaningfulUserText(text)
		if first == "" || (commandFallback && candidate != "") {
			first = candidate
			commandFallback = strings.Contains(strings.ToLower(text), "<command-name>") && !strings.Contains(candidate, " ")
		}
	}
	return first, continued
}

type displayText struct {
	RequestID string   `json:"request_id"`
	Texts     []string `json:"texts"`
}

const userTextItems = `arraySlice(arrayFilter((x,d,r,t)->d='input' AND r='user' AND t='message',msg_text,msg_direction,msg_role,msg_type),1,32)`

const displayTextColumns = `arrayMap(x->if(length(x)<=4000,x,concat(substring(x,1,2000),'\n',substring(x,-2000))),arrayMap(x->` + displayPreambleSQL + `,` + userTextItems + `)) AS texts`

// Remove wrappers before truncation so a long injected policy cannot hide the prompt.
const displayPolicyPattern = `'(?is)^\\s*(?:<current_datetime>.*?(?:</current_datetime>\\s*|$)|<system>.*?(?:</system>\\s*|$)|<developer>.*?(?:</developer>\\s*|$))+'`
const displayPolicySQL = `replaceRegexpOne(x,` + displayPolicyPattern + `,'')`
const displayPreambleSQL = `if(startsWith(lowerUTF8(x),'some of the conversation history has been summarized'),arrayFirst(t->trimBoth(t)!='',arrayMap(x->` + displayPolicySQL + `,extractAll(x,'(?is)<user_message>(.*?)</user_message>'))),` + displayPolicySQL + `)`

func (c *clickhouseReader) displayTexts(ctx context.Context, w readWindow, ids []string) (map[string]displayText, error) {
	out := map[string]displayText{}
	// Eight bounded arrays fit below the reader's result cap even with JSON escaping.
	for start := 0; start < len(ids); start += 8 {
		params := url.Values{}
		for k, v := range w.params {
			params[k] = append([]string(nil), v...)
		}
		keys := []string{}
		for i, id := range ids[start:min(start+8, len(ids))] {
			key := "text_id_" + strconv.Itoa(i)
			params.Set(key, id)
			keys = append(keys, "{"+key+":String}")
		}
		var rows []displayText
		err := c.query(ctx, `SELECT request_id,`+displayTextColumns+` FROM {db:Identifier}.request_facts PREWHERE `+timeFilter+` AND request_id IN (`+strings.Join(keys, ",")+`) LIMIT 8`, params, &rows)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.RequestID] = row
		}
	}
	return out, nil
}

const searchWhitespacePattern = `'[\\s\\x{000b}\\x{0085}\\x{00a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}]+'`

const searchTextMatch = `arrayExists(x->positionCaseInsensitiveUTF8(replaceRegexpAll(replaceRegexpAll(decodeHTMLComponent(x),'(?is)<t3_context[^>]*>.*?</t3_context>',''),` + searchWhitespacePattern + `,' '),{search:String})>0
 OR (position(x,'t3-citation://')>0 AND positionCaseInsensitiveUTF8(replaceRegexpAll(decodeURLComponent(replaceAll(x,'+',' ')),` + searchWhitespacePattern + `,' '),{search:String})>0)
 OR (startsWith({search:String},'/') AND position(x,'<command-name>')>0),` + userTextItems + `)`

const searchTurnStart = `(NOT ` + sideCall + ` AND (NOT prior_seen OR (NOT ` + tailHasTool + ` AND ` + tailHasUser + `)))`

// Walk candidate conversations in activity order without rereading earlier text pages.
func (c *clickhouseReader) searchSessions(ctx context.Context, w readWindow, source, filters string, wanted int, showSystem bool) ([]string, error) {
	present, err := c.turnSearchExists(ctx)
	if err != nil {
		return nil, err
	}
	compactFilter := ""
	if present {
		p := cloneValues(w.params)
		p.Set("search_pattern", "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(p.Get("search")))+"%")
		lookup := ` FROM {db:Identifier}.turn_search WHERE ` + timeFilter + ` AND text LIKE {search_pattern:String}
 UNION ALL SELECT request_id,conversation_id FROM {db:Identifier}.turn_search WHERE ` + timeFilter + ` AND (overflow OR (startsWith({search:String},'/') AND has_command))`
		candidates := `SELECT DISTINCT request_id,conversation_id FROM (SELECT request_id,conversation_id` + lookup + `)`
		var matches []struct {
			RequestID string `json:"request_id"`
		}
		if err := c.query(ctx, candidates+` LIMIT 1`, p, &matches); err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, nil
		}
		filters += ` AND conversation_id IN (SELECT conversation_id FROM (` + candidates + `))`
		compactFilter = ` AND request_id IN (SELECT request_id FROM (` + candidates + `))`
		w.params = p
	}
	const size = 80
	ids := []string{}
	seek := ""
	for {
		w.params.Set("candidates", strconv.Itoa(size))
		var candidates []struct {
			ID string `json:"id"`
			Lo string `json:"lo"`
			Hi string `json:"hi"`
		}
		err := c.query(ctx, `SELECT conversation_id AS id,toString(min(started_at)) AS lo,toString(addNanoseconds(max(started_at),1)) AS hi FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+` AND conversation_id IN (SELECT conversation_id `+source+` WHERE `+filters+` GROUP BY conversation_id) GROUP BY conversation_id`+seek+` ORDER BY max(started_at) DESC,conversation_id LIMIT {candidates:UInt32}`, w.params, &candidates)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return ids, nil
		}
		candidateIDs := []string{}
		lo, hi := candidates[0].Lo, candidates[0].Hi
		for _, row := range candidates {
			candidateIDs = append(candidateIDs, row.ID)
			if row.Lo < lo {
				lo = row.Lo
			}
			if row.Hi > hi {
				hi = row.Hi
			}
		}
		encoded, _ := json.Marshal(candidateIDs)
		w.params.Set("candidate_ids", string(encoded))

		// Restrict the primary time key before reading candidate text.
		w.params.Set("search_from", lo)
		w.params.Set("search_to", hi)
		startRule := searchTurnStart
		if present {
			// The earliest hash is only needed for conversations on this candidate page.
			startRule = strings.Replace(startRule, " GROUP BY conversation_id", ` AND conversation_id IN (SELECT arrayJoin(JSONExtract({candidate_ids:String},'Array(String)'))) GROUP BY conversation_id`, 1)
		}
		matches, err := c.matchingConversations(ctx, w, `started_at >= {search_from:DateTime64(9,'UTC')} AND started_at < {search_to:DateTime64(9,'UTC')} AND conversation_id IN (SELECT arrayJoin(JSONExtract({candidate_ids:String},'Array(String)'))) AND `+conversationTraffic+` AND `+startRule+compactFilter, size, present)
		if err != nil {
			return nil, err
		}
		if !showSystem && len(matches) > 0 {
			encoded, _ := json.Marshal(matches)
			w.params.Set("system_candidate_ids", string(encoded))
			var hidden []struct {
				ID string `json:"id"`
			}
			err := c.query(ctx, `SELECT conversation_id AS id FROM {db:Identifier}.request_facts PREWHERE `+timeFilter+` AND conversation_id IN (SELECT arrayJoin(JSONExtract({system_candidate_ids:String},'Array(String)'))) AND `+conversationTraffic+` GROUP BY conversation_id HAVING max(`+systemSession+`)`, w.params, &hidden)
			if err != nil {
				return nil, err
			}
			excluded := map[string]bool{}
			for _, row := range hidden {
				excluded[row.ID] = true
			}
			visible := []string{}
			for _, id := range matches {
				if !excluded[id] {
					visible = append(visible, id)
				}
			}
			matches = visible
		}
		found := map[string]bool{}
		for _, id := range matches {
			found[id] = true
		}
		for _, row := range candidates {
			if found[row.ID] {
				ids = append(ids, row.ID)
			}
		}
		if len(ids) >= wanted || len(candidates) < size {
			return ids[:min(len(ids), wanted)], nil
		}
		last := candidates[len(candidates)-1]
		w.params.Set("candidate_before", last.Hi)
		w.params.Set("candidate_after_id", last.ID)
		seek = ` HAVING max(started_at)<subtractNanoseconds({candidate_before:DateTime64(9,'UTC')},1)
 OR (max(started_at)=subtractNanoseconds({candidate_before:DateTime64(9,'UTC')},1) AND conversation_id>{candidate_after_id:String})`
	}
}

func (c *clickhouseReader) matchingConversations(ctx context.Context, w readWindow, where string, wanted int, compact bool) ([]string, error) {
	found := map[string]bool{}
	ids := []string{}
	p := cloneValues(w.params)
	seek := ""
	for scanned := 0; scanned < 1000000; {
		var rows []struct {
			ID        string    `json:"id"`
			RequestID string    `json:"request_id"`
			StartedAt time.Time `json:"started_at"`
			Bytes     uint64    `json:"bytes"`
		}
		textItems, table, predicate := userTextItems, "request_facts", where+seek+` AND `+searchTextMatch
		if compact {
			textItems, table = "texts", "turn_search"
			predicate = timeFilter + seek + ` AND request_id IN (SELECT request_id FROM {db:Identifier}.request_facts PREWHERE ` + where + `)`
		}
		err := c.query(ctx, `SELECT conversation_id AS id,request_id,started_at,length(toJSONString(`+textItems+`)) AS bytes FROM {db:Identifier}.`+table+` PREWHERE `+predicate+` ORDER BY started_at DESC,request_id DESC LIMIT 512 SETTINGS max_threads=1,max_block_size=8`, p, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return ids, nil
		}
		batch := 0
		bytes := uint64(0)
		keys := []string{}
		for _, row := range rows {
			if row.Bytes <= 1<<20 {
				if bytes+row.Bytes > 1<<20 {
					break
				}
				bytes += row.Bytes
				keys = append(keys, row.RequestID)
			}
			batch++
		}
		texts := map[string][]string{}
		if len(keys) > 0 {
			encoded, _ := json.Marshal(keys)
			p.Set("text_ids", string(encoded))
			var values []displayText
			err = c.query(ctx, `SELECT request_id,`+textItems+` AS texts FROM {db:Identifier}.`+table+` PREWHERE `+timeFilter+` AND request_id IN (SELECT arrayJoin(JSONExtract({text_ids:String},'Array(String)'))) LIMIT 512 SETTINGS max_threads=1,max_block_size=8`, p, &values)
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				texts[value.RequestID] = value.Texts
			}
		}
		for _, row := range rows[:batch] {
			items := texts[row.RequestID]
			if row.Bytes > 1<<20 {
				full := cloneValues(w.params)
				full.Set("from", row.StartedAt.Format("2006-01-02 15:04:05.999999999"))
				full.Set("to", row.StartedAt.Add(time.Nanosecond).Format("2006-01-02 15:04:05.999999999"))
				full.Set("id", row.RequestID)
				data, err := c.rawBytes(ctx, readWindow{params: full}, "toJSONString("+textItems+")", ` FROM {db:Identifier}.`+table+` WHERE `+timeFilter+` AND request_id={id:String}`)
				if err != nil {
					return nil, err
				}
				if err = json.Unmarshal(data, &items); err != nil {
					return nil, err
				}
			}
			text, _ := firstUserText(items)
			if !found[row.ID] && strings.Contains(strings.ToLower(strings.Join(strings.Fields(text), " ")), strings.ToLower(p.Get("search"))) {
				found[row.ID] = true
				ids = append(ids, row.ID)
			}
		}
		if (batch == len(rows) && len(rows) < 512) || len(ids) >= wanted {
			return ids, nil
		}
		last := rows[batch-1]
		p.Set("text_before", last.StartedAt.Format("2006-01-02 15:04:05.999999999"))
		p.Set("text_before_id", last.RequestID)
		seek = ` AND (started_at,request_id)<({text_before:DateTime64(9,'UTC')},{text_before_id:String})`
		scanned += batch
	}
	return ids, nil
}

func (c *clickhouseReader) turnSearchExists(ctx context.Context) (bool, error) {
	c.turnSearchMu.Lock()
	defer c.turnSearchMu.Unlock()
	if time.Since(c.turnSearchChecked) < 30*time.Second {
		return c.turnSearchPresent, nil
	}
	var rows []struct {
		Present uint64 `json:"present"`
	}
	err := c.query(ctx, `SELECT count() AS present FROM system.tables WHERE database={database:String} AND name='turn_search' AND comment='archive-turn-search-v1-ready'`, url.Values{"database": {c.config.Database}}, &rows)
	if err == nil {
		c.turnSearchChecked = time.Now()
		c.turnSearchPresent = len(rows) > 0 && rows[0].Present > 0
	}
	return c.turnSearchPresent, err
}
