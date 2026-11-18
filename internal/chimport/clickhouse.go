package chimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type client struct {
	endpoint, user, password string
	http                     *http.Client
}

func (c *client) call(ctx context.Context, q string, params url.Values, body io.Reader, compressed bool) ([]byte, error) {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid ClickHouse URL")
	}
	v := u.Query()
	v.Set("query", q)
	v.Set("wait_end_of_query", "1")
	v.Set("date_time_input_format", "best_effort")
	v.Set("output_format_json_quote_64bit_integers", "0")
	for k, vs := range params {
		v[k] = vs
	}
	u.RawQuery = v.Encode()
	var payload []byte
	if body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}
	var result []byte
	err = retry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("invalid ClickHouse request")
		}
		req.SetBasicAuth(c.user, c.password)
		if compressed {
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return transientError{"ClickHouse transport failed"}
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return transientError{"ClickHouse read failed"}
		}
		if resp.StatusCode >= 500 {
			return transientError{fmt.Sprintf("ClickHouse HTTP %d (details suppressed)", resp.StatusCode)}
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("ClickHouse HTTP %d (details suppressed)", resp.StatusCode)
		}
		result = b
		return nil
	})
	return result, err
}
func (c *client) insert(ctx context.Context, table string, rows []json.RawMessage) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	var plain bytes.Buffer
	for _, item := range rows {
		plain.Write(item)
		plain.WriteByte('\n')
	}
	sum := sha256.Sum256(plain.Bytes())
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write(plain.Bytes()); err != nil {
		return 0, err
	}
	if err := z.Close(); err != nil {
		return 0, err
	}
	_, err := c.call(ctx, "INSERT INTO cliproxy."+table+" FORMAT JSONEachRow", url.Values{"insert_deduplication_token": {"archive-v1-" + table + "-" + hex.EncodeToString(sum[:])}, "insert_deduplicate": {"1"}}, &buf, true)
	return int64(plain.Len()), err
}

func (c *client) diskStatus(ctx context.Context, minFree uint64, log io.Writer) error {
	b, err := c.call(ctx, "SELECT min(free_space) AS free FROM system.disks FORMAT JSONEachRow", nil, nil, false)
	if err != nil {
		return err
	}
	var disk struct {
		Free uint64 `json:"free"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(b), &disk); err != nil {
		return err
	}
	b, err = c.call(ctx, "SELECT table,count() AS parts,sum(bytes_on_disk) AS disk_bytes FROM system.parts WHERE active AND database='cliproxy' GROUP BY table ORDER BY table FORMAT JSONEachRow", nil, nil, false)
	if err != nil {
		return err
	}
	if log != nil {
		fmt.Fprintf(log, "disk_free_bytes=%d active_parts=%s\n", disk.Free, bytes.TrimSpace(b))
	}
	if disk.Free < minFree {
		return fmt.Errorf("disk guard stopped import: free bytes %d below threshold %d", disk.Free, minFree)
	}
	return nil
}

func idsParam(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "'" + strings.ReplaceAll(strings.ReplaceAll(id, "\\", "\\\\"), "'", "\\'") + "'"
	}
	return "[" + strings.Join(out, ",") + "]"
}
func (c *client) existing(ctx context.Context, table string, ids []string) (map[string]bool, error) {
	params := url.Values{"param_ids": {idsParam(ids)}}
	q := "SELECT request_id FROM cliproxy." + table + " WHERE request_id IN {ids:Array(String)} FORMAT JSONEachRow"
	if table == "requests" {
		q = "SELECT request_id, request_headers['x-plana-import']='archive-v1' AS imported FROM cliproxy.requests WHERE request_id IN {ids:Array(String)} FORMAT JSONEachRow"
	}
	b, err := c.call(ctx, q, params, nil, false)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var row struct {
			ID       string `json:"request_id"`
			Imported uint8  `json:"imported"`
		}
		err = dec.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		out[row.ID] = row.Imported != 0
	}
	return out, nil
}
func newClient(endpoint, user, password string) *client {
	return &client{endpoint, user, password, &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

const externalDDL = `CREATE TABLE IF NOT EXISTS cliproxy.external_messages (
 session_id String,native_session_id String,source LowCardinality(String),project String,title String,
 ts DateTime64(9,'UTC'),position UInt32,role LowCardinality(String),type LowCardinality(String),
 tool_name LowCardinality(String),text String CODEC(ZSTD(3)),ingested_at DateTime64(9,'UTC') DEFAULT now64(9)
) ENGINE=ReplacingMergeTree(ingested_at) ORDER BY (session_id,position)`
