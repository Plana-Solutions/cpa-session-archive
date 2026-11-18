package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

func (c *clickhouseReader) export(ctx context.Context, w readWindow, q url.Values) (any, error) {
	format := q.Get("format")
	if format != "" && format != "archive" && format != "sft" {
		return nil, badQuery("unsupported export format")
	}
	if format == "" {
		format = "archive"
	}
	if q.Get("scope") != "all" && q.Get("id") == "" {
		return nil, badQuery("session id is required")
	}
	ids := []struct {
		ID string `json:"id"`
	}{}
	where := timeFilter
	if q.Get("id") != "" {
		w.params.Set("session", q.Get("id"))
		where += ` AND conversation_id={session:String}`
	}
	sql := `SELECT request_id AS id FROM {db:Identifier}.request_facts WHERE ` + where
	present, err := c.externalExists(ctx)
	if err != nil {
		return nil, err
	}
	if present {
		externalWhere := `ts >= {from:DateTime64(9,'UTC')} AND ts < {to:DateTime64(9,'UTC')}`
		if q.Get("id") != "" {
			w.params.Set("external_session", strings.TrimPrefix(q.Get("id"), "external:"))
			externalWhere += ` AND session_id={external_session:String}`
		}
		sql += ` UNION ALL SELECT concat('external:',session_id,':',toString(position)) AS id FROM {db:Identifier}.external_messages FINAL WHERE ` + externalWhere
	}
	err = c.query(ctx, `SELECT id FROM (`+sql+`) ORDER BY id LIMIT 201`, w.params, &ids)
	if err != nil {
		return nil, err
	}
	if len(ids) > 200 {
		return nil, badQuery("Export exceeds 200 records. Narrow the date range or export one session. Use offline tools for larger archives.")
	}
	var content strings.Builder
	for _, row := range ids {
		value, err := c.request(ctx, w, url.Values{"id": {row.ID}}, "requests", 1)
		if err != nil {
			return nil, err
		}
		rec := value.(Record)
		var item any = rec
		if format == "sft" {
			example, valid := trainingExample(rec)
			if !valid {
				continue
			}
			item = example
		}
		line, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if content.Len()+len(line) > resultLimit/2 {
			return nil, badQuery("Export is too large. Narrow the range or use offline tools.")
		}
		content.Write(line)
		content.WriteByte('\n')
	}
	name := "cpa-session-archive.all"
	if q.Get("id") != "" {
		name = "session"
	}
	return map[string]string{"content": content.String(), "filename": name + "." + format + ".jsonl", "content_type": "application/x-ndjson"}, nil
}
