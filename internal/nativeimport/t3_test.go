package nativeimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestT3ExportsHistoryTypesExcludesAuthAndProjectsNativeItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statev2.sqlite")
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE auth_sessions(id TEXT, secret TEXT)`,
		`INSERT INTO auth_sessions VALUES('excluded','never-archive-auth-fixture')`,
		`CREATE TABLE orchestration_events(id INTEGER, payload_json TEXT, original_blob BLOB, optional TEXT, weight REAL)`,
		`INSERT INTO orchestration_events VALUES(1,'{"type":"unknown-event","spacing": "retain"}',x'0001ff',NULL,1.5)`,
		`CREATE TABLE orchestration_v2_projection_threads(thread_id TEXT,title TEXT,created_at TEXT,updated_at TEXT,payload_json TEXT)`,
		`INSERT INTO orchestration_v2_projection_threads VALUES('thread-a','Archive fixture','2026-10-22T10:00:00Z','2026-10-22T10:00:03Z','{"worktreePath":"/fixture/project","lineage":{"parentThreadId":"parent-a"}}')`,
		`CREATE TABLE orchestration_v2_projection_turn_items(turn_item_id TEXT,thread_id TEXT,parent_item_id TEXT,ordinal INTEGER,type TEXT,updated_at TEXT,payload_json TEXT)`,
		`INSERT INTO orchestration_v2_projection_turn_items VALUES('item-a','thread-a',NULL,1,'dynamic_tool','2026-10-22T10:00:01Z','{"toolName":"shell","input":"pwd","output":"/fixture/project","startedAt":"2026-10-22T10:00:00Z"}')`,
		`CREATE TABLE projection_thread_messages(message_id TEXT,thread_id TEXT,role TEXT,text TEXT,created_at TEXT)`,
		`INSERT INTO projection_thread_messages VALUES('legacy-a','thread-a','assistant','Duplicate fallback','2026-10-22T10:00:02Z')`,
		`INSERT INTO projection_thread_messages VALUES('legacy-b','legacy-thread','user','Legacy fixture','2026-10-22T10:00:03Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Prepare(context.Background(), archive.ExternalSource{Tool: "t3", Path: path}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	raw, err := io.ReadAll(artifact.File)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auth_sessions") || strings.Contains(string(raw), "never-archive-auth-fixture") {
		t.Fatal("authentication data entered history export")
	}
	if !strings.Contains(string(raw), `"type":"blob","value":"AAH/"`) || !strings.Contains(string(raw), `"type":"null","value":null`) || !strings.Contains(string(raw), `"type":"real","value":1.5`) {
		t.Fatal("SQLite runtime types or binary bytes lost")
	}
	if artifact.Source.Format != "t3-history-export-v1" || len(artifact.Warnings) != 2 || len(artifact.Sessions) != 2 {
		t.Fatal("incorrect export scope or projected sessions")
	}
	for _, session := range artifact.Sessions {
		if session.ID == "thread-a" {
			if len(session.Events) != 1 || session.ParentID != "parent-a" || session.ProjectPath != "/fixture/project" || session.Events[0].Role != "dynamic_tool" || !strings.Contains(session.Events[0].Text, "pwd") {
				t.Fatal("native item, project, parent or legacy dedup failed")
			}
			var original map[string]json.RawMessage
			if json.Unmarshal(session.Events[0].Data, &original) != nil || stringValue(original["turn_item_id"]) != "item-a" {
				t.Fatal("original turn item row not retained")
			}
		}
	}
	after, err := os.Stat(path)
	if err != nil || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatal("source database changed during read-only export", err)
	}
}

func TestT3RefusesUnknownDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-history.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE auth_sessions(secret TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Prepare(context.Background(), archive.ExternalSource{Tool: "t3", Path: path}, false)
	if err == nil || !strings.Contains(err.Error(), "no recognized T3 history tables") {
		t.Fatal("unknown database silently archived", err)
	}
}

func TestT3MutableEventRevisionAndMessageRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE orchestration_v2_projection_turn_items(turn_item_id TEXT,thread_id TEXT,parent_item_id TEXT,ordinal INTEGER,type TEXT,updated_at TEXT,payload_json TEXT);
		INSERT INTO orchestration_v2_projection_turn_items VALUES('item','thread',NULL,1,'user_message','2026-10-22T10:00:00Z','{"text":"first"}')`); err != nil {
		t.Fatal(err)
	}
	prepare := func() *Artifact {
		t.Helper()
		a, err := Prepare(context.Background(), archive.ExternalSource{Tool: "t3", Path: path}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	a, same := prepare(), prepare()
	if a.Sessions[0].Events[0].ID != same.Sessions[0].Events[0].ID || a.Sessions[0].Events[0].Role != "user" {
		t.Fatal("stable revision or normalized message role missing")
	}
	if _, err = db.Exec(`UPDATE orchestration_v2_projection_turn_items SET payload_json='{"text":"changed"}'`); err != nil {
		t.Fatal(err)
	}
	b := prepare()
	if a.Sessions[0].Events[0].ID == b.Sessions[0].Events[0].ID {
		t.Fatal("mutable native event revision would overwrite archival identity")
	}
}
