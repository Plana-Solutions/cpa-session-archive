package nativeimport

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cpa-session-archive/internal/archive"
)

func parseJSONL(ctx context.Context, r io.Reader, source archive.ExternalSource) ([]archive.ExternalSession, error) {
	reader := bufio.NewReader(r)
	var sessions []archive.ExternalSession
	byID := map[string]int{}
	canonical := codexFileID(source.Path)
	var offset int64
	for line := 1; ; line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, readErr := reader.ReadBytes('\n')
		start := offset
		offset += int64(len(data))
		if len(strings.TrimSpace(string(data))) != 0 {
			var row map[string]json.RawMessage
			if err := json.Unmarshal(data, &row); err != nil {
				return nil, fmt.Errorf("invalid JSONL at line %d byte %d: %w", line, start, err)
			}
			var sessionID, parent, project, title string
			var event archive.ExternalEvent
			if source.Tool == "codex" {
				payload := object(row["payload"])
				if canonical == "" && stringValue(row["type"]) == "session_meta" {
					canonical = firstString(payload, "id", "session_id")
				}
				sessionID = canonical
				parent = firstString(payload, "parent_thread_id", "forked_from_id")
				project = stringValue(payload["cwd"])
				event = codexEvent(row)
			} else {
				sessionID = stringValue(row["sessionId"])
				parent = firstString(row, "parentSessionId", "parent_session_id")
				agentID := stringValue(row["agentId"])
				if agentID == "" && strings.Contains(filepath.ToSlash(source.Path), "/subagents/") {
					agentID = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(source.Path), "agent-"), ".jsonl")
				}
				if agentID != "" {
					parent = sessionID
					sessionID += "/agent/" + agentID
				}
				project = stringValue(row["cwd"])
				title = firstString(row, "customTitle", "title")
				event = claudeEvent(row)
			}
			if sessionID == "" {
				sessionID = "file-" + shortHash(source.Path)
			}
			if event.ID == "" {
				event.ID = fmt.Sprintf("byte-%d", start)
			}
			event.Data = append(json.RawMessage(nil), data...)
			idx, exists := byID[sessionID]
			if !exists {
				idx = len(sessions)
				byID[sessionID] = idx
				sessions = append(sessions, archive.ExternalSession{ID: sessionID})
			}
			s := &sessions[idx]
			if parent != "" {
				s.ParentID = parent
			}
			if project != "" {
				s.ProjectPath = project
			}
			if title != "" {
				s.Title = title
			}
			s.Events = append(s.Events, event)
			includeTime(s, event.Timestamp)
		}
		if readErr == io.EOF {
			return sessions, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read JSONL at line %d: %w", line, readErr)
		}
	}
}

var rolloutID = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

func codexFileID(path string) string {
	if match := rolloutID.FindStringSubmatch(filepath.Base(path)); len(match) == 2 {
		return match[1]
	}
	return ""
}

func codexEvent(row map[string]json.RawMessage) archive.ExternalEvent {
	payload := object(row["payload"])
	kind := stringValue(payload["type"])
	outer := stringValue(row["type"])
	e := archive.ExternalEvent{ID: stringValue(payload["id"]), Timestamp: parseTime(row["timestamp"]), Role: outer}
	if e.Timestamp.IsZero() {
		e.Timestamp = parseTime(payload["timestamp"])
	}
	if outer != "response_item" {
		e.Text = firstString(payload, "message", "text", "summary")
		if e.ID == "" && len(row["ordinal"]) != 0 {
			e.ID = "ordinal-" + string(row["ordinal"])
		}
		return e
	}
	e.Role = kind
	e.Name, e.CallID = stringValue(payload["name"]), stringValue(payload["call_id"])
	switch kind {
	case "message", "agent_message":
		e.Role = firstString(payload, "role", "author")
		e.Text = contentText(payload["content"])
	case "function_call", "custom_tool_call":
		e.Role = "tool_call"
		e.Text = firstString(payload, "arguments", "input")
	case "function_call_output", "custom_tool_call_output":
		e.Role = "tool_result"
		e.Text = contentText(payload["output"])
	case "reasoning":
		e.Text = contentText(payload["summary"])
	default:
		e.Text = contentText(payload["content"])
	}
	if e.Role == "" {
		e.Role = outer
	}
	return e
}

func claudeEvent(row map[string]json.RawMessage) archive.ExternalEvent {
	message := object(row["message"])
	e := archive.ExternalEvent{ID: stringValue(row["uuid"]), ParentID: stringValue(row["parentUuid"]), Timestamp: parseTime(row["timestamp"]), Role: stringValue(row["type"])}
	if role := stringValue(message["role"]); role != "" {
		e.Role = role
	}
	e.Text = contentText(message["content"])
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(message["content"], &blocks) == nil && len(blocks) == 1 {
		switch stringValue(blocks[0]["type"]) {
		case "tool_use":
			e.Role, e.Name, e.CallID = "tool_call", stringValue(blocks[0]["name"]), stringValue(blocks[0]["id"])
		case "tool_result":
			e.Role, e.CallID = "tool_result", stringValue(blocks[0]["tool_use_id"])
		}
	}
	if e.Text == "" {
		e.Text = firstString(row, "content", "text", "summary", "title")
	}
	return e
}

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		typeName := stringValue(block["type"])
		text := firstString(block, "text", "thinking", "summary")
		switch typeName {
		case "tool_use":
			text = stringValue(block["name"]) + "\n" + string(block["input"])
		case "tool_result":
			text = contentText(block["content"])
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}

func stringValue(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func firstString(row map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if s := stringValue(row[key]); s != "" {
			return s
		}
	}
	return ""
}

func parseTime(raw json.RawMessage) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, stringValue(raw))
	return t
}

func includeTime(session *archive.ExternalSession, timestamp time.Time) {
	if timestamp.IsZero() {
		return
	}
	if session.StartedAt.IsZero() || timestamp.Before(session.StartedAt) {
		session.StartedAt = timestamp
	}
	if session.CompletedAt.IsZero() || timestamp.After(session.CompletedAt) {
		session.CompletedAt = timestamp
	}
}

func shortHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])[:24]
}
