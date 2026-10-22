package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestCLIImportReplayAndLosslessSourceDownload(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "rollout.jsonl")
	database := filepath.Join(dir, "archive.sqlite")
	raw := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"cli-fixture\"}}\n{\"timestamp\":\"2026-10-22T10:00:00Z\",\"type\":\"response_item\",\"payload\":{\"id\":\"message-1\",\"type\":\"message\",\"role\":\"user\",\"content\":\"Fixture CLI request\"}}\n")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--db", database, "--source", source, "--host", "fixture-host", "--account", "fixture-account", "--tool", "codex"}
	var first, second bytes.Buffer
	if err := run(context.Background(), args, &first); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Receipt archive.ExternalImportReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(first.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Events != 2 || result.Receipt.ImportedEvents != 2 || bytes.Contains(first.Bytes(), []byte("Fixture CLI request")) {
		t.Fatal("unexpected receipt or transcript leaked into CLI output")
	}
	if err := run(context.Background(), args, &second); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Receipt.Replayed || result.Receipt.ImportedEvents != 0 {
		t.Fatal("identical source replay inserted duplicate native events")
	}
	store, err := archive.OpenStore(database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	var restored bytes.Buffer
	if err := store.WriteExternalSource(context.Background(), result.Receipt.Source.ID, &restored); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	if !bytes.Equal(raw, restored.Bytes()) || result.Receipt.Source.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatal("raw source receipt/download differs from original bytes")
	}
}

func TestCLIRequiresProvenanceAndBlocksWholeT3Raw(t *testing.T) {
	if err := run(context.Background(), []string{"--source", "fixture.jsonl"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing source provenance accepted")
	}
	args := []string{"--db", "archive.sqlite", "--source", "state.sqlite", "--host", "fixture-host", "--account", "fixture-account", "--tool", "t3", "--raw-only"}
	if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("whole T3 database raw import accepted")
	}
}

func TestCLIPreservesTransferredPathAndRejectsSourceDestinationAlias(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(source, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"transfer-fixture\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--db", source, "--source", source, "--host", "fixture-host", "--account", "fixture-account", "--tool", "codex"}
	if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("source/destination alias accepted")
	}
	args[1] = filepath.Join(dir, "archive.sqlite")
	args = append(args, "--original-path", "/original/account/rollout.jsonl")
	var output bytes.Buffer
	if err := run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Receipt archive.ExternalImportReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Source.Source.Path != "/original/account/rollout.jsonl" {
		t.Fatal("transferred source original path lost")
	}
}
