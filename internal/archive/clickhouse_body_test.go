package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
)

func TestClickHouseMixedBodies(t *testing.T) {
	c := localClickHouse(t)
	var module struct{ Dir string }
	cmd := exec.Command("go", "list", "-m", "-json", "github.com/Plana-Solutions/cpa-clickhouse-sink")
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &module); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err = filepath.WalkDir(module.Dir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(module.Dir, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/sink-fixture_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "archive_fixture_test.go"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		ID      string
		Body    []byte
		Dedup   bool
		Lineage string
	}{
		{"legacy", []byte(" {\"messages\" : [{\"role\":\"user\", \"content\":\"legacy\\n字\"}]}\n"), false, "mixed"},
		{"dedup", []byte(" {\"messages\" : [{\"role\":\"user\", \"content\":\"exact\\n字\"}],\"tools\":[{}]}\n"), true, "mixed"},
		{"binary", []byte{255, 0, 128, '{', '"'}, true, "mixed"},
		{"empty", nil, true, "mixed"},
		{"large", []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 7<<20) + `"}]}`), true, "large"},
		{"missing", []byte(`{"input":["missing"]}`), true, "missing"},
		{"corrupt", []byte(`{"input":["corrupt"]}`), true, "corrupt"},
		{"digest", []byte(`{"input":["digest"]}`), true, "digest"},
	}
	b, _ = json.Marshal(rows)
	input := filepath.Join(dir, "input.json")
	if err = os.WriteFile(input, b, 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-run", "^TestArchiveFixture$", "-count=1", "-v", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ARCHIVE_SINK_INPUT="+input, "ARCHIVE_SINK_URL="+c.config.URL)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("pinned sink writer: %v\n%s", e, output)
	}
	for _, file := range []string{"derived.sql"} {
		b, e := os.ReadFile("testdata/clickhouse/" + file)
		if e != nil {
			t.Fatal(e)
		}
		for _, sql := range strings.Split(string(b), "\n;\n") {
			chExec(t, c, strings.ReplaceAll(sql, "{db}", "cliproxy"))
		}
	}
	chExec(t, c, `INSERT INTO cliproxy.request_facts (request_id,started_at,ingested_at,conversation_id,kind,status_code,outcome,input_items,msg_direction,msg_position,msg_role,msg_type,msg_text,msg_tool_name,msg_call_id,msg_arguments,msg_is_error,msg_text_hash) SELECT request_id,started_at,started_at,concat('fixture-',request_id),'messages',200,'succeeded',200,['input','output','output'],[0,0,1],['user','tool','assistant'],['message','function_call_output','message'],['fixture prompt','answer','final answer'],['','',''],['','call-test',''],['','',''],[false,false,false],[0,0,0] FROM cliproxy.requests`)
	chExec(t, c, `ALTER TABLE cliproxy.body_blobs DELETE WHERE lineage=`+strconv.FormatUint(bodystore.LineageKey("missing"), 10)+` SETTINGS mutations_sync=1`)
	// Same hash in another lineage must not mask a missing blob.
	sk, blobs := bodystore.Split([]byte(`{"input":["missing"]}`))
	for _, h := range sk.Refs {
		chExec(t, c, fmt.Sprintf("INSERT INTO cliproxy.body_blobs (lineage,hash,data,first_seen) VALUES (%d,unhex('%x'),unhex('%x'),now64(9))", bodystore.LineageKey("other"), h[:], blobs[h]))
	}
	chExec(t, c, `ALTER TABLE cliproxy.body_blobs UPDATE data='wrong' WHERE lineage=`+strconv.FormatUint(bodystore.LineageKey("corrupt"), 10)+` SETTINGS mutations_sync=1`)
	chExec(t, c, `ALTER TABLE cliproxy.requests UPDATE body_sha256=repeat('x',32) WHERE request_id='digest' SETTINGS mutations_sync=1`)
	for _, row := range rows {
		t.Run(row.ID, func(t *testing.T) {
			value, err := c.read("requests", url.Values{"id": {row.ID}})
			broken := row.ID == "missing" || row.ID == "corrupt" || row.ID == "digest"
			if broken {
				if !errors.Is(err, bodystore.ErrIncomplete) {
					t.Fatalf("want body incomplete, got %v", err)
				}
				if value != nil && len(value.(Record).OriginalRequest) > 0 {
					t.Fatal("exposed corrupt body")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(value.(Record).OriginalRequest, row.Body) {
					t.Fatal("body bytes differ")
				}
			}
			if _, err = c.read("request-context", url.Values{"id": {row.ID}, "call_id": {"call-test"}}); err != nil {
				t.Fatalf("context integrity: %v", err)
			}
			for _, format := range []string{"archive", "sft"} {
				q := url.Values{"id": {"fixture-" + row.ID}, "format": {format}}
				if broken {
					ticket, e := c.read("export", q)
					if e != nil || ticket.(map[string]any)["skipped_incomplete"] != 1 || ticket.(map[string]any)["next_offset"] != 1 {
						t.Fatalf("%s did not skip incomplete body: %v %v", format, ticket, e)
					}
					continue
				}
				content, _ := collectExport(t, c, q)
				if format == "sft" {
					var exported struct {
						Messages []struct {
							Role, Content string
							ToolCallID    string `json:"tool_call_id"`
						}
					}
					wantPrompt := "fixture prompt"
					input, _ := trainingExample(Record{OriginalRequest: row.Body})
					if len(input.Messages) > 0 {
						wantPrompt, _ = input.Messages[0]["content"].(string)
					}
					if json.Unmarshal([]byte(content), &exported) != nil || len(exported.Messages) != 3 || exported.Messages[0].Content != wantPrompt || exported.Messages[1].Content != "answer" || exported.Messages[1].ToolCallID != "call-test" || exported.Messages[2].Content != "final answer" {
						t.Fatal("mixed-format SFT changed messages")
					}
				}
				if format == "archive" {
					if strings.Contains(content, `"original_request":null`) || strings.Contains(content, `"response":null`) {
						t.Fatal("archive bodies must remain base64 strings")
					}
					var record struct {
						Body []byte `json:"original_request"`
					}
					if json.Unmarshal([]byte(content), &record) != nil || !bytes.Equal(record.Body, row.Body) {
						t.Fatal("archive bytes differ")
					}
				}
			}
		})
	}
	for _, format := range []string{"archive", "sft"} {
		content, _ := collectExport(t, c, url.Values{"scope": {"all"}, "format": {format}})
		if content == "" {
			t.Fatal("incomplete body aborted all-session export")
		}
	}
	// During migration both physical formats can be visible. Prefer and verify the skeleton.
	chExec(t, c, `INSERT INTO cliproxy.requests (request_id,started_at,completed_at,request_body) SELECT request_id,started_at,completed_at,'wrong legacy copy' FROM cliproxy.requests WHERE request_id='dedup'`)
	got, err := c.read("requests", url.Values{"id": {"dedup"}})
	if err != nil || !bytes.Equal(got.(Record).OriginalRequest, rows[1].Body) {
		t.Fatal("migration copy preference", err)
	}

	chExec(t, c, `ALTER TABLE cliproxy.request_facts UPDATE conversation_id='fixture-dedup' WHERE request_id='corrupt' SETTINGS mutations_sync=1`)
	if _, err = c.read("request-context", url.Values{"id": {"dedup"}, "call_id": {"call-test"}}); err != nil {
		t.Fatalf("derived context depends on raw body: %v", err)
	}

}
