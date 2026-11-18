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

const displayTextColumns = `arrayMap(x->if(length(x)<=4000,x,concat(substring(x,1,2000),'\n',substring(x,-2000))),` + userTextItems + `) AS texts`

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

// Search the most recently active candidates first. Common terms stop at the page;
// rare terms expand the candidate set. Titles and search share the same cleanup.
func (c *clickhouseReader) searchSessions(ctx context.Context, w readWindow, source, filters string, wanted int) ([]string, error) {
	for size := max(80, wanted); ; size *= 8 {
		if size > 640 {
			return c.matchingConversations(ctx, w, timeFilter+` AND `+conversationTraffic+` AND conversation_id!='' AND `+turnStart, 1000000)
		}

		w.params.Set("candidates", strconv.Itoa(size))
		var candidates []struct {
			ID string `json:"id"`
			Lo string `json:"lo"`
			Hi string `json:"hi"`
		}
		err := c.query(ctx, `SELECT conversation_id AS id,toString(min(started_at)) AS lo,toString(addNanoseconds(max(started_at),1)) AS hi FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND `+conversationTraffic+` AND conversation_id IN (SELECT conversation_id `+source+` WHERE `+filters+` GROUP BY conversation_id) GROUP BY conversation_id ORDER BY max(started_at) DESC,conversation_id LIMIT {candidates:UInt32}`, w.params, &candidates)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return nil, nil
		}
		ids := []string{}
		lo, hi := candidates[0].Lo, candidates[0].Hi
		for _, row := range candidates {
			ids = append(ids, row.ID)
			if row.Lo < lo {
				lo = row.Lo
			}
			if row.Hi > hi {
				hi = row.Hi
			}
		}
		encoded, _ := json.Marshal(ids)
		w.params.Set("candidate_ids", string(encoded))

		// Restrict the primary time key before reading candidate text.
		w.params.Set("search_from", lo)
		w.params.Set("search_to", hi)
		matches, err := c.matchingConversations(ctx, w, `started_at >= {search_from:DateTime64(9,'UTC')} AND started_at < {search_to:DateTime64(9,'UTC')} AND conversation_id IN (SELECT arrayJoin(JSONExtract({candidate_ids:String},'Array(String)'))) AND `+conversationTraffic+` AND `+turnStart, size)
		if err != nil {
			return nil, err
		}
		if len(matches) >= wanted || len(candidates) < size {
			found := map[string]bool{}
			for _, id := range matches {
				found[id] = true
			}
			ids := []string{}
			for _, row := range candidates {
				if found[row.ID] {
					ids = append(ids, row.ID)
				}
			}
			return ids[:min(len(ids), wanted)], nil
		}
	}
}

func (c *clickhouseReader) matchingConversations(ctx context.Context, w readWindow, where string, wanted int) ([]string, error) {
	found := map[string]bool{}
	ids := []string{}
	p := cloneValues(w.params)
	for offset := 0; offset < 1000000; offset += 8 {
		p.Set("text_offset", strconv.Itoa(offset))
		var rows []struct {
			ID         string    `json:"id"`
			RequestID  string    `json:"request_id"`
			StartedAt  time.Time `json:"started_at"`
			Incomplete uint8     `json:"incomplete"`
			Texts      []string  `json:"texts"`
		}
		err := c.query(ctx, `SELECT conversation_id AS id,request_id,started_at,arraySum(arrayMap(x->length(x),`+userTextItems+`))>100000 AS incomplete,if(incomplete,[],`+userTextItems+`) AS texts FROM {db:Identifier}.request_facts PREWHERE `+where+` ORDER BY started_at DESC,request_id LIMIT 8 OFFSET {text_offset:UInt32} SETTINGS max_threads=1,max_block_size=8`, p, &rows)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Incomplete != 0 {
				full := cloneValues(w.params)
				full.Set("from", row.StartedAt.Format("2006-01-02 15:04:05.999999999"))
				full.Set("to", row.StartedAt.Add(time.Nanosecond).Format("2006-01-02 15:04:05.999999999"))
				full.Set("id", row.RequestID)
				data, err := c.rawBytes(ctx, readWindow{params: full}, "toJSONString("+userTextItems+")", ` FROM {db:Identifier}.request_facts WHERE `+timeFilter+` AND request_id={id:String}`)
				if err != nil {
					return nil, err
				}
				if err = json.Unmarshal(data, &row.Texts); err != nil {
					return nil, err
				}
			}
			text, _ := firstUserText(row.Texts)
			if !found[row.ID] && strings.Contains(strings.ToLower(text), strings.ToLower(p.Get("search"))) {
				found[row.ID] = true
				ids = append(ids, row.ID)
			}
		}
		if len(rows) < 8 || len(ids) >= wanted {
			return ids, nil
		}
	}
	return ids, nil
}
