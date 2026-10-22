package nativeimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"cpa-session-archive/internal/archive"
)

var t3HistoryTables = []string{
	"orchestration_events", "orchestration_v2_events",
	"orchestration_v2_projection_threads", "orchestration_v2_projection_messages",
	"orchestration_v2_projection_turn_items", "orchestration_v2_projection_runs",
	"orchestration_v2_projection_run_attempts", "orchestration_v2_projection_nodes",
	"orchestration_v2_projection_subagents", "orchestration_v2_projection_provider_threads",
	"orchestration_v2_projection_context_handoffs", "orchestration_v2_projection_checkpoints",
	"projection_threads", "projection_thread_messages", "projection_thread_activities",
	"projection_turns", "projection_checkpoints", "projection_pending_approvals",
	"projection_proposed_plans",
}

func prepareT3(ctx context.Context, source string, raw io.Writer) ([]archive.ExternalSession, []string, error) {
	u := url.URL{Scheme: "file", Path: source}
	u.RawQuery = "mode=ro&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	tables, err := exportT3History(ctx, tx, raw)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := projectT3(ctx, tx, tables)
	warnings := []string{"History-table export only; authentication, pairing, webhooks and configuration tables excluded. Source database bytes are not archived.", "Searchable view uses native turn items and legacy message/activity fallback; all allowlisted history rows remain in the downloadable export."}
	return sessions, warnings, err
}

type t3Query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func exportT3History(ctx context.Context, db t3Query, out io.Writer) (map[string]bool, error) {
	encoder := json.NewEncoder(out)
	if err := encoder.Encode(map[string]any{"format": "t3-history-export-v1", "sqlite_values": "Each cell carries its runtime storage type; blob values use base64."}); err != nil {
		return nil, err
	}
	tables := make(map[string]bool)
	for _, table := range t3HistoryTables {
		var schema string
		err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&schema)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		tables[table] = true
		rows, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		if err := encoder.Encode(map[string]any{"table": table, "schema": schema, "columns": columns}); err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return nil, err
			}
			cells := make([]map[string]any, len(values))
			for i, value := range values {
				typeName := "null"
				switch value.(type) {
				case int64:
					typeName = "integer"
				case float64:
					typeName = "real"
				case string:
					typeName = "text"
				case []byte:
					typeName = "blob"
				}
				cells[i] = map[string]any{"type": typeName, "value": value}
			}
			if err := encoder.Encode(map[string]any{"table": table, "values": cells}); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("no recognized T3 history tables; refusing a whole-database archive")
	}
	return tables, nil
}

func projectT3(ctx context.Context, db t3Query, tables map[string]bool) ([]archive.ExternalSession, error) {
	sessions := map[string]*archive.ExternalSession{}
	get := func(id string) *archive.ExternalSession {
		if sessions[id] == nil {
			sessions[id] = &archive.ExternalSession{ID: id}
		}
		return sessions[id]
	}
	for _, table := range []string{"projection_threads", "orchestration_v2_projection_threads"} {
		if !tables[table] {
			continue
		}
		if err := eachT3Row(ctx, db, table, "thread_id", func(row map[string]json.RawMessage) {
			s := get(stringValue(row["thread_id"]))
			s.Title = stringValue(row["title"])
			s.ProjectPath = stringValue(row["worktree_path"])
			payload := object(row["payload_json"])
			if embedded := stringValue(row["payload_json"]); embedded != "" {
				payload = object(json.RawMessage(embedded))
			}
			if path := stringValue(payload["worktreePath"]); path != "" {
				s.ProjectPath = path
			}
			s.ParentID = stringValue(object(payload["lineage"])["parentThreadId"])
			includeTime(s, parseTime(row["created_at"]))
			includeTime(s, parseTime(row["updated_at"]))
		}); err != nil {
			return nil, err
		}
	}
	covered := map[string]bool{}
	if tables["orchestration_v2_projection_turn_items"] {
		err := eachT3Row(ctx, db, "orchestration_v2_projection_turn_items", "thread_id,ordinal,turn_item_id", func(row map[string]json.RawMessage) {
			id := stringValue(row["thread_id"])
			s := get(id)
			covered[id] = true
			payload := object(json.RawMessage(stringValue(row["payload_json"])))
			e := archive.ExternalEvent{ID: stringValue(row["turn_item_id"]), ParentID: stringValue(row["parent_item_id"]), Role: stringValue(row["type"]), Data: marshalRow(row), Timestamp: parseTime(payload["startedAt"])}
			e.ID += ":revision:" + shortHash(string(e.Data))
			switch e.Role {
			case "user_message":
				e.Role = "user"
			case "assistant_message":
				e.Role = "assistant"
			}
			if e.Timestamp.IsZero() {
				e.Timestamp = parseTime(row["updated_at"])
			}
			e.Text = t3Text(payload)
			e.Name = firstString(payload, "toolName", "title")
			e.CallID = firstString(payload, "callId", "toolCallId")
			s.Events = append(s.Events, e)
			includeTime(s, e.Timestamp)
		})
		if err != nil {
			return nil, err
		}
	}
	for _, spec := range []struct{ table, order, id string }{
		{"projection_thread_messages", "thread_id,created_at,message_id", "message_id"},
		{"projection_thread_activities", "thread_id,sequence,activity_id", "activity_id"},
	} {
		if !tables[spec.table] {
			continue
		}
		err := eachT3Row(ctx, db, spec.table, spec.order, func(row map[string]json.RawMessage) {
			id := stringValue(row["thread_id"])
			if covered[id] {
				return
			}
			s := get(id)
			e := archive.ExternalEvent{ID: stringValue(row[spec.id]), Role: firstString(row, "role", "kind"), Text: firstString(row, "text", "summary"), Timestamp: parseTime(row["created_at"]), Data: marshalRow(row)}
			e.ID += ":revision:" + shortHash(string(e.Data))
			s.Events = append(s.Events, e)
			includeTime(s, e.Timestamp)
		})
		if err != nil {
			return nil, err
		}
	}
	var result []archive.ExternalSession
	for _, s := range sessions {
		if len(s.Events) != 0 {
			result = append(result, *s)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func eachT3Row(ctx context.Context, db t3Query, table, order string, handle func(map[string]json.RawMessage)) error {
	rows, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY `+order)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		row := make(map[string]json.RawMessage, len(columns))
		for i, column := range columns {
			if b, ok := values[i].([]byte); ok {
				values[i] = string(b)
			}
			row[column], err = json.Marshal(values[i])
			if err != nil {
				return err
			}
		}
		handle(row)
	}
	return rows.Err()
}

func marshalRow(row map[string]json.RawMessage) json.RawMessage {
	data, _ := json.Marshal(row)
	return data
}

func t3Text(payload map[string]json.RawMessage) string {
	var parts []string
	for _, key := range []string{"text", "input", "output", "diffStr", "summary", "reasoning", "prompt", "result"} {
		if text := contentText(payload[key]); text != "" {
			parts = append(parts, text)
		} else if raw := payload[key]; len(raw) != 0 && string(raw) != "null" {
			parts = append(parts, string(raw))
		}
	}
	return strings.Join(parts, "\n\n")
}
