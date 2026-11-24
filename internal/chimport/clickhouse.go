package chimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type client struct {
	endpoint, user, password string
	runID                    string
	http                     *http.Client
	responseLimit            int64
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
	v.Set("use_query_condition_cache", "0")
	u.RawQuery = v.Encode()
	var result []byte
	err = retry(ctx, func() error {
		if seek, ok := body.(io.Seeker); ok {
			if _, err := seek.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
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
		limit := c.responseLimit
		if limit == 0 {
			limit = 128 << 20
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if int64(len(b)) > limit {
			return fmt.Errorf("ClickHouse response exceeds %d bytes", limit)
		}
		if err != nil {
			return transientError{"ClickHouse read failed"}
		}
		code := resp.Header.Get("X-ClickHouse-Exception-Code")
		if resp.StatusCode >= 500 || code == "241" || code == "252" {
			return transientError{fmt.Sprintf("ClickHouse HTTP %d exception_code=%q (details suppressed)", resp.StatusCode, code)}
		}
		if resp.StatusCode != 200 {
			return httpError{resp.StatusCode, code}
		}
		result = b
		return nil
	})
	return result, err
}

type httpError struct {
	status int
	code   string
}

func (e httpError) Error() string {
	return fmt.Sprintf("ClickHouse HTTP %d exception_code=%q (details suppressed)", e.status, e.code)
}
func badInsert(err error) bool {
	var e httpError
	if !errors.As(err, &e) || e.status != http.StatusBadRequest {
		return false
	}
	switch e.code {
	// ClickHouse input parsing errors; schema and resource failures remain fatal.
	case "6", "7", "9", "26", "27", "38", "41", "72", "117", "130", "131":
		return true
	}
	return false
}

func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	err := e.Encode(v)
	return b.Bytes(), err
}

func (c *client) insert(ctx context.Context, table string, rows [][]byte) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if c.runID == "" {
		return 0, errors.New("insert requires a checkpoint run ID")
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	sum := sha256.New()
	dst := io.MultiWriter(z, sum)
	var size int64
	for _, row := range rows {
		n, err := dst.Write(row)
		if err != nil {
			return 0, err
		}
		size += int64(n)
	}
	if err := z.Close(); err != nil {
		return 0, err
	}
	query := "INSERT INTO cliproxy." + table + " FORMAT JSONEachRow"
	if table == "requests" {
		query = "INSERT INTO cliproxy.requests (" + bodyRequestColumns + ") FORMAT RowBinary"
	}
	if table == "body_blobs" {
		query = "INSERT INTO cliproxy.body_blobs (lineage,hash,data,first_seen) FORMAT RowBinary"
	}
	_, err := c.call(ctx, query, url.Values{"insert_deduplication_token": {"archive-v3-" + c.runID + "-" + table + "-" + hex.EncodeToString(sum.Sum(nil))}, "insert_deduplicate": {"1"}}, bytes.NewReader(buf.Bytes()), true)
	return size, err
}

// A rejected single row is durable quarantine; transport and server failures stop the batch.
func (c *client) insertSplit(ctx context.Context, table string, rows [][]byte, ids []int64, quarantine func(int64, string) error) (int, int64, error) {
	if len(rows) == 0 {
		return 0, 0, nil
	}
	b, err := c.insert(ctx, table, rows)
	if err == nil {
		return len(rows), b, nil
	}
	if !badInsert(err) {
		return 0, 0, err
	}
	if len(rows) == 1 {
		return 0, 0, quarantine(ids[0], err.Error())
	}
	mid := len(rows) / 2
	n, b, err := c.insertSplit(ctx, table, rows[:mid], ids[:mid], quarantine)
	if err != nil {
		return n, b, err
	}
	n2, b2, err := c.insertSplit(ctx, table, rows[mid:], ids[mid:], quarantine)
	return n + n2, b + b2, err
}

func (c *client) diskStatus(ctx context.Context, minFree uint64, log io.Writer) error {
	b, err := c.call(ctx, "SELECT min(unreserved_space) AS free FROM system.disks FORMAT JSONEachRow", nil, nil, false)
	if err != nil {
		return fmt.Errorf("disk preflight requires GRANT SELECT(unreserved_space) ON system.disks: %w", err)
	}
	var disk struct {
		Free uint64 `json:"free"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(b), &disk); err != nil {
		return err
	}
	b, err = c.call(ctx, "SELECT table,partition,count() AS parts,sum(bytes_on_disk) AS disk_bytes FROM system.parts WHERE active AND database='cliproxy' GROUP BY table,partition ORDER BY table,partition FORMAT JSONEachRow", nil, nil, false)
	if err != nil {
		return fmt.Errorf("parts preflight requires GRANT SELECT(database,table,partition,active,bytes_on_disk) ON system.parts: %w", err)
	}
	if log != nil {
		fmt.Fprintf(log, "disk_free_bytes=%d active_parts=%s\n", disk.Free, bytes.TrimSpace(b))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var part struct {
			Parts uint64 `json:"parts"`
		}
		err := dec.Decode(&part)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if part.Parts > 300 {
			return fmt.Errorf("parts guard stopped import: partition has %d parts", part.Parts)
		}
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
func (c *client) existing(ctx context.Context, table string, ids []string, lo, hi string) (map[string]bool, error) {
	params := url.Values{"param_ids": {idsParam(ids)}, "param_lo": {lo}, "param_hi": {hi}}
	q := "SELECT request_id FROM cliproxy." + table + " WHERE request_id IN {ids:Array(String)} FORMAT JSONEachRow"
	if table == "requests" {
		q = "SELECT request_id, min(request_headers['x-plana-import']='archive-v1') AS imported FROM cliproxy.requests WHERE request_id IN {ids:Array(String)} GROUP BY request_id FORMAT JSONEachRow"
	}
	field := "requested_at"
	if table == "requests" {
		field = "started_at"
	}
	q = strings.Replace(q, " WHERE ", " WHERE "+field+">=subtractDays({lo:DateTime64(9,'UTC')},1) AND "+field+"<=addDays({hi:DateTime64(9,'UTC')},1) AND ", 1)
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
func newClient(endpoint, user, password, runID string) *client {
	return &client{runID: runID, endpoint: endpoint, user: user, password: password, http: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) schemaPreflight(ctx context.Context) error {
	for _, target := range []struct{ table, columns string }{
		{"requests", bodyRequestColumns},
		{"body_blobs", "lineage,hash,data,first_seen"},
		{"external_messages", "import_marker,session_id,native_session_id,source,project,title,ts,position,role,type,tool_name,text"},
	} {
		if _, err := c.call(ctx, "SELECT "+target.columns+" FROM cliproxy."+target.table+" LIMIT 0", nil, nil, false); err != nil {
			return fmt.Errorf("schema preflight refused cliproxy.%s: deploy the v0.1.0-plana.9 dedup schema and external_messages before import: %w", target.table, err)
		}
	}
	return nil
}
