package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestPrivateT3BundleTransportExcludesAuthAndRetainsProvenance(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "state.sqlite")
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE auth_sessions(secret TEXT); INSERT INTO auth_sessions VALUES('excluded-auth-fixture');
		CREATE TABLE orchestration_v2_projection_turn_items(turn_item_id TEXT,thread_id TEXT,parent_item_id TEXT,ordinal INTEGER,type TEXT,updated_at TEXT,payload_json TEXT);
		INSERT INTO orchestration_v2_projection_turn_items VALUES('item','session',NULL,1,'user_message','2026-10-22T10:00:00Z','{"text":"bundle fixture question"}')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "private.tar")
	args := []string{"--bundle-out", bundle, "--source", source, "--host", "m1", "--account", "minoo", "--tool", "t3"}
	var prepared bytes.Buffer
	if err = run(context.Background(), args, &prepared); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("excluded-auth-fixture")) || bytes.Contains(raw, []byte("auth_sessions")) || bytes.Contains(prepared.Bytes(), []byte("bundle fixture question")) {
		t.Fatal("bundle or receipt violated private history boundary")
	}
	info, _ := os.Stat(bundle)
	if info.Mode().Perm() != 0600 {
		t.Fatal("bundle permissions must be private")
	}
	if err = run(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing bundle overwritten")
	}
	destination := filepath.Join(dir, "archive.sqlite")
	var receipt bytes.Buffer
	if err = run(context.Background(), []string{"--bundle", bundle, "--db", destination}, &receipt); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Receipt archive.ExternalImportReceipt `json:"receipt"`
	}
	if err = json.Unmarshal(receipt.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Receipt.Source.Source.Path != source || out.Receipt.Source.Source.Account != "minoo" || out.Receipt.Events != 1 {
		t.Fatal("transport lost original provenance or projection")
	}
	store, err := archive.OpenStore(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	var restored bytes.Buffer
	if err = store.WriteExternalSource(context.Background(), out.Receipt.Source.ID, &restored); err != nil || !strings.Contains(restored.String(), "orchestration_v2_projection_turn_items") {
		t.Fatal("transported history export cannot be recovered", err)
	}
	index := bytes.LastIndex(raw, []byte("bundle fixture question"))
	if index < 0 {
		t.Fatal("missing raw history fixture")
	}
	raw[index] = 'X'
	corrupt := filepath.Join(dir, "corrupt.tar")
	if err = os.WriteFile(corrupt, raw, 0600); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"--bundle", corrupt, "--db", filepath.Join(dir, "reject.sqlite")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatal("corrupted source bundle accepted", err)
	}
}
