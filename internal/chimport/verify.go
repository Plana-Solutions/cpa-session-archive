package chimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/deriver/extract"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
)

// VerifySample reads only a bounded throwaway sample and emits no body content.
func VerifySample(ctx context.Context, o Options, out io.Writer) error {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return errors.New("verification requires isolated loopback URL")
	}
	c := newClient(o.URL, o.User, o.Password)
	host, err := c.call(ctx, "SELECT hostName() FORMAT TabSeparated", nil, nil, false)
	if err != nil {
		return err
	}
	if string(host) != "cpa-archive-import-sample\n" {
		return errors.New("verification refuses non-sample host")
	}
	b, err := os.ReadFile(o.State)
	if err != nil {
		return err
	}
	var s checkpoint
	if err = json.Unmarshal(b, &s); err != nil {
		return err
	}
	store, err := openReadOnly(o.SQLite)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	ids, err := readIDs(ctx, store.DB, `SELECT id,request_id,started_at FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' AND (coalesce(model,'')<>'' OR coalesce(requested_model,'')<>'') ORDER BY id LIMIT 10001`, s.FirstID, s.UpperID)
	if err != nil {
		return err
	}
	if len(ids) == 0 || len(ids) > 10000 {
		return errors.New("verification requires 1..10000 sample rows")
	}
	if err = validateSources(o.Config, o.AuthDir); err != nil {
		return err
	}
	r, err := redact.New()
	if err != nil {
		return err
	}
	r.Configure([]string{o.Config, o.AuthDir}, nil)
	reader := bodystore.Reader{Database: "cliproxy", Query: func(ctx context.Context, q string, p map[string]string) (io.ReadCloser, error) {
		params := url.Values{}
		for k, v := range p {
			params.Set("param_"+k, v)
		}
		b, err := c.call(ctx, q, params, nil, false)
		return io.NopCloser(bytes.NewReader(b)), err
	}}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	var batch []requestRow
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		keys := []string{}
		expected := map[string]requestRow{}
		for _, row := range batch {
			keys = append(keys, row.RequestID)
			expected[row.RequestID] = row
		}
		b, err := c.call(ctx, "SELECT request_id,"+bodystore.Projection+" FROM cliproxy.requests WHERE request_id IN {ids:Array(String)} FORMAT JSONEachRow", url.Values{"param_ids": {idsParam(keys)}}, nil, false)
		if err != nil {
			return err
		}
		blobs, err := reader.Load(ctx, keys)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		for {
			var stored struct {
				RequestID string `json:"request_id"`
				bodystore.Body
			}
			err := dec.Decode(&stored)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			row, ok := expected[stored.RequestID]
			if !ok {
				return errors.New("unexpected or duplicate sample request")
			}
			body, err := stored.Restore(blobs)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(row.RequestBody))
			digest := hex.EncodeToString(sum[:])
			if !bytes.Equal(body, []byte(row.RequestBody)) || !strings.EqualFold(stored.SHA, digest) {
				return errors.New("redacted archive body differs from stored body or digest")
			}
			f := extract.Extract(extractRow(row), false)
			if err = enc.Encode(map[string]any{"request_id": row.RequestID, "body_sha256": digest, "byte_exact": true, "conversation_id": f.ConversationID, "client": f.Client}); err != nil {
				return err
			}
			delete(expected, stored.RequestID)
		}
		if len(expected) != 0 {
			return errors.New("sample requests missing")
		}
		batch = nil
		batchBytes = 0
		return nil
	}
	for _, x := range ids {
		a, err := readArchive(ctx, store.DB, x.RequestID)
		if err != nil {
			return err
		}
		row, _, err := mapRecord(a, r)
		if err != nil {
			return err
		}
		if len(batch) >= 16 || batchFull(batchBytes, len(row.RequestBody), 16<<20) {
			if err = flush(); err != nil {
				return err
			}
		}
		// Only redacted request bytes are retained between reads.
		row.ResponseBody = ""
		row.ResponseChunks = nil
		batch = append(batch, row)
		batchBytes += len(row.RequestBody)
	}
	return flush()
}
