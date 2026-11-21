package chimport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cpa-session-archive/internal/archive"
	"github.com/Plana-Solutions/cpa-clickhouse-sink/redact"
	"gopkg.in/yaml.v3"
)

type Options struct {
	SQLite, State, URL, User, Password, Config, AuthDir string
	Batch, Newest, ExternalSessions                     int
	Rate                                                float64
	BatchBytes                                          int
	MinFreeBytes                                        uint64
	DiskCheckBatches                                    int
	Log                                                 io.Writer
	FirstID, UpperID                                    int64
	MaxQuarantined                                      int
	RetryQuarantined                                    bool
}
type checkpoint struct {
	Identity        string `json:"identity"`
	RunID           string `json:"run_id"`
	LastID, UpperID int64
	FirstID         int64
	ExternalSession string
	ExternalDone    bool
	NativeLastID    int64
	NativePosition  uint32
	EmptySkipped    int64 `json:"empty_skipped"`
	NoModelSkipped  int64 `json:"no_model_skipped"`
	NoOriginalRef   int64 `json:"no_original_ref"`
	Quarantine      map[int64]string
}
type item struct {
	ID        int64
	RequestID string
	StartedAt string
}

const requestPath = `lower(COALESCE(nullif(json_extract(metadata_json,'$.request_path'),''),(SELECT value FROM record_facets WHERE record_facets.request_id=records.request_id AND record_facets.name='request.path' LIMIT 1),''))`

// Auxiliary endpoints do not represent model conversations. Other model-less
// rows remain importable so the archive preserves real chat calls.
const modelLessAuxiliary = `(source_format IN ('models','embeddings') OR ` + requestPath + ` LIKE '%count_tokens%' OR ` + requestPath + ` LIKE '%counttokens%' OR ` + requestPath + ` LIKE '%/models%' OR ` + requestPath + ` LIKE '%/embeddings%' OR ` + requestPath + ` LIKE '%embedcontent%')`
const emptyModelLess = `(COALESCE(model,'')='' AND COALESCE(requested_model,'')='' AND ` + requestPath + `='' AND COALESCE(original_ref,'')='' AND COALESCE(length(original_request_gz),0)=0 AND COALESCE(response_ref,'')='' AND COALESCE(length(response_gz),0)=0)`
const skippedEmptyRows = `source_format!='external-native-event' AND ` + emptyModelLess
const importableProxyRows = `source_format!='external-native-event' AND ((COALESCE(model,'')<>'' OR COALESCE(requested_model,'')<>'') OR NOT ` + modelLessAuxiliary + `) AND NOT ` + emptyModelLess
const skippedModelLessRows = `source_format!='external-native-event' AND COALESCE(model,'')='' AND COALESCE(requested_model,'')='' AND ` + modelLessAuxiliary

func openReadOnly(path string) (*archive.Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_query_only=1&_busy_timeout=1000"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &archive.Store{DB: db, DBPath: abs}, nil
}
func save(path string, s checkpoint) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(path+".tmp", path)
}
func loadState(o Options, db *sql.DB) (checkpoint, error) {
	abs, _ := filepath.Abs(o.SQLite)
	sum := sha256.Sum256([]byte(fmt.Sprintf("archive-v1|%s|%s|%s|%d|%d|%d|%d", abs, o.URL, o.User, o.Newest, o.ExternalSessions, o.FirstID, o.UpperID)))
	s := checkpoint{Identity: hex.EncodeToString(sum[:]), Quarantine: map[int64]string{}}
	b, err := os.ReadFile(o.State)
	if err == nil {
		var old checkpoint
		if err = json.Unmarshal(b, &old); err != nil {
			return s, err
		}
		if old.Identity != s.Identity {
			return s, errors.New("state belongs to a different source, target or sample")
		}
		if old.RunID == "" {
			return s, errors.New("checkpoint predates run-scoped deduplication; use a fresh state file")
		}
		if old.Quarantine == nil {
			old.Quarantine = map[int64]string{}
		}
		return old, nil
	}
	if !os.IsNotExist(err) {
		return s, err
	}
	if err = db.QueryRow("SELECT coalesce(max(id),0) FROM records").Scan(&s.UpperID); err != nil {
		return s, err
	}
	if o.UpperID > 0 && o.UpperID < s.UpperID {
		s.UpperID = o.UpperID
	}
	if o.FirstID > 0 {
		s.LastID = o.FirstID - 1
	}
	if o.Newest > 0 {
		var first sql.NullInt64
		err = db.QueryRow("SELECT min(id) FROM (SELECT id FROM records WHERE source_format!='external-native-event' AND id<=? ORDER BY id DESC LIMIT ?)", s.UpperID, o.Newest).Scan(&first)
		if err != nil {
			return s, err
		}
		if first.Valid {
			s.LastID = first.Int64 - 1
		} else {
			s.LastID = s.UpperID
		}
	}
	s.FirstID = s.LastID
	s.RunID = rand.Text()
	return s, save(o.State, s)
}
func readIDs(ctx context.Context, db *sql.DB, q string, args ...any) (out []item, err error) {
	err = retry(ctx, func() error { var e error; out, e = readIDsOnce(ctx, db, q, args...); return e })
	return
}
func readIDsOnce(ctx context.Context, db *sql.DB, q string, args ...any) ([]item, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []item{}
	for rows.Next() {
		var x item
		var started sql.NullString
		if err = rows.Scan(&x.ID, &x.RequestID, &started); err != nil {
			return nil, err
		}
		x.StartedAt = started.String
		out = append(out, x)
	}
	return out, rows.Err()
}
func validateSources(config, auth string) error {
	b, err := os.ReadFile(config)
	if err != nil {
		return errors.New("CPA config unreadable")
	}
	var configValue map[string]any
	if yaml.Unmarshal(b, &configValue) != nil {
		return errors.New("CPA config YAML invalid")
	}
	if len(b) == 0 {
		return errors.New("CPA config empty")
	}
	entries, err := os.ReadDir(auth)
	if err != nil {
		return errors.New("CPA auth directory unreadable")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err = os.ReadFile(filepath.Join(auth, e.Name()))
		if err != nil {
			return errors.New("CPA auth file unreadable")
		}
		if filepath.Ext(e.Name()) == ".json" && !json.Valid(b) {
			return errors.New("CPA auth JSON invalid")
		}
	}
	return nil
}
func Run(ctx context.Context, o Options) error {
	if o.Batch < 1 || o.Batch > 128 || o.Newest < 0 || o.ExternalSessions < -1 || o.Rate < 0 || o.SQLite == "" || o.State == "" {
		return errors.New("invalid import options")
	}
	if o.BatchBytes == 0 {
		o.BatchBytes = 16 << 20
	}
	if o.DiskCheckBatches == 0 {
		o.DiskCheckBatches = 20
	}
	if o.MaxQuarantined == 0 {
		o.MaxQuarantined = 10
	}
	if o.MaxQuarantined < 1 || o.FirstID < 0 || o.UpperID < 0 || (o.Newest > 0 && o.FirstID > 0) {
		return errors.New("invalid sample or quarantine limits")
	}
	if o.BatchBytes < 1 || o.DiskCheckBatches < 1 {
		return errors.New("invalid batch limits")
	}
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" {
		return errors.New("invalid ClickHouse URL")
	}
	if err = validateSources(o.Config, o.AuthDir); err != nil {
		return err
	}
	r, err := redact.New()
	if err != nil {
		return errors.New("redactor unavailable")
	}
	r.Configure([]string{o.Config, o.AuthDir}, nil)
	store, err := openReadOnly(o.SQLite)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	var s checkpoint
	err = retry(ctx, func() error { var e error; s, e = loadState(o, store.DB); return e })
	if err != nil {
		return err
	}
	originalProgress := s
	persist := func() error {
		cp := s
		if o.RetryQuarantined {
			cp.LastID = originalProgress.LastID
			cp.ExternalSession = originalProgress.ExternalSession
			cp.NativeLastID = originalProgress.NativeLastID
			cp.NativePosition = originalProgress.NativePosition
			cp.ExternalDone = originalProgress.ExternalDone
		}
		return save(o.State, cp)
	}
	batchBad := map[int64]bool{}
	quarantine := func(id int64, reason string) error {
		batchBad[id] = true
		s.Quarantine[id] = reason
		if err := persist(); err != nil {
			return err
		}
		if o.Log != nil {
			fmt.Fprintf(o.Log, "quarantine_id=%d reason=%s\n", id, reason)
		}
		if len(s.Quarantine) >= o.MaxQuarantined {
			return errors.New("quarantine limit reached")
		}
		return nil
	}
	finish := func() error {
		if len(s.Quarantine) > 0 {
			return fmt.Errorf("import incomplete: %d quarantined rows", len(s.Quarantine))
		}
		return nil
	}
	retryFilter := ""
	if o.RetryQuarantined {
		if len(s.Quarantine) == 0 {
			return nil
		}
		ids := []string{}
		for id := range s.Quarantine {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		retryFilter = " AND id IN (" + strings.Join(ids, ",") + ")"
		s.LastID = s.FirstID
		s.ExternalSession = ""
		s.NativeLastID = 0
		s.NativePosition = 0
		s.ExternalDone = false
	}
	c := newClient(o.URL, o.User, o.Password, s.RunID)
	if err = c.schemaPreflight(ctx); err != nil {
		return err
	}
	start := time.Now()
	batchStart := start
	var rowsN, nativeN, bytesN, skipped int64
	batches := 0
	if err = c.diskStatus(ctx, o.MinFreeBytes, o.Log); err != nil {
		return err
	}
	var noModel int64
	err = retry(ctx, func() error {
		return store.DB.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE id>? AND id<=? AND "+skippedModelLessRows, s.FirstID, s.UpperID).Scan(&noModel)
	})
	if err != nil {
		return err
	}
	s.NoModelSkipped = noModel
	if err = retry(ctx, func() error {
		return store.DB.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE id>? AND id<=? AND "+skippedEmptyRows, s.FirstID, s.UpperID).Scan(&s.EmptySkipped)
	}); err != nil {
		return err
	}
	var noOriginalRef int64
	err = retry(ctx, func() error {
		var e error
		e = store.DB.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' AND COALESCE(original_ref,'')=''", s.FirstID, s.UpperID).Scan(&noOriginalRef)
		return e
	})
	if err != nil {
		return err
	}
	s.NoOriginalRef = noOriginalRef
	if o.Log != nil {
		fmt.Fprintf(o.Log, "source_no_original_ref=%d source_no_model=%d source_empty=%d\n", s.NoOriginalRef, s.NoModelSkipped, s.EmptySkipped)
	}
	stopCtx := ctx
	ctx = context.WithoutCancel(ctx) // Finish and checkpoint the current batch before honoring a stop signal.
	progress := func(n int, b int64, native bool) error {
		bytesN += b
		if native {
			nativeN += int64(n)
		} else {
			rowsN += int64(n)
		}
		if !native && o.Rate > 0 {
			delay := time.Duration(float64(n)/o.Rate*float64(time.Second)) - time.Since(batchStart)
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-stopCtx.Done():
					timer.Stop()
					return stopCtx.Err()
				case <-timer.C:
				}
			}
		}
		if o.Log != nil {
			elapsed := time.Since(start).Seconds()
			fmt.Fprintf(o.Log, "proxy_rows=%d native_events=%d skipped_existing=%d skipped_no_model=%d no_original_ref=%d quarantined=%d last_id=%d rows/s=%.2f MB/s=%.2f elapsed=%s\n", rowsN, nativeN, skipped, s.NoModelSkipped, s.NoOriginalRef, len(s.Quarantine), s.LastID, float64(rowsN+nativeN)/elapsed, float64(bytesN)/1e6/elapsed, time.Since(start).Round(time.Millisecond))
		}
		batchStart = time.Now()
		batches++
		return nil
	}
	guard := func() error {
		if err := stopCtx.Err(); err != nil {
			return err
		}
		if batches > 0 && batches%o.DiskCheckBatches == 0 {
			return c.diskStatus(ctx, o.MinFreeBytes, o.Log)
		}
		return stopCtx.Err()
	}
	readRecord := func(x item) (a archive.Record, err error) {
		err = retry(ctx, func() error { var e error; a, e = readArchive(ctx, store.DB, x.RequestID); return e })
		return
	}
	type prepared struct {
		id     int64
		rb, ub []byte
		body   bodyItems
		size   int
	}
	var pending *prepared
	for {
		if err = guard(); err != nil {
			return err
		}
		ids, err := readIDs(ctx, store.DB, "SELECT id,request_id,started_at FROM records WHERE id>? AND id<=? AND "+importableProxyRows+retryFilter+" ORDER BY id LIMIT ?", s.LastID, s.UpperID, o.Batch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			s.LastID = s.UpperID
			if err = persist(); err != nil {
				return err
			}
			break
		}
		keys := []string{}
		ukeys := []string{}
		for _, x := range ids {
			keys = append(keys, x.RequestID)
			ukeys = append(ukeys, "archive-v1:"+x.RequestID)
		}
		lo, hi := "", ""
		for _, x := range ids {
			if ts, e := time.Parse(time.RFC3339Nano, x.StartedAt); e == nil {
				t := stamp(ts)
				if lo == "" || t < lo {
					lo = t
				}
				if hi == "" || t > hi {
					hi = t
				}
			}
		}
		if lo == "" {
			lo = "1970-01-01 00:00:00.000000000"
			hi = lo
		}
		present, err := c.existing(ctx, "requests", keys, lo, hi)
		if err != nil {
			return err
		}
		used, err := c.existing(ctx, "usage", ukeys, lo, hi)
		if err != nil {
			return err
		}
		batchBad = map[int64]bool{}
		var requests, usage [][]byte
		var requestIDs, usageIDs []int64
		var bodies []bodyItems
		batchBytes := 0
		last := s.LastID
		for _, x := range ids {
			if batchBytes >= o.BatchBytes {
				break
			}
			imported, exists := present[x.RequestID]
			_, hasUsage := used["archive-v1:"+x.RequestID]
			if _, bad := s.Quarantine[x.ID]; bad && !o.RetryQuarantined {
				last = x.ID
				continue
			}
			if exists && (!imported || hasUsage) {
				if o.RetryQuarantined {
					delete(s.Quarantine, x.ID)
				}
				skipped++
				last = x.ID
				continue
			}
			var rb, ub []byte
			var body bodyItems
			size := 0
			if pending != nil && pending.id == x.ID {
				rb, ub, body, size = pending.rb, pending.ub, pending.body, pending.size
				pending = nil
				if exists {
					rb = nil
					body = bodyItems{}
				}
				if hasUsage {
					ub = nil
				}
			} else {
				stopProgress := recordProgress(o.Log, x.ID, 30*time.Second)
				a, err := readRecord(x)
				if err != nil {
					stopProgress()
				}
				if err != nil {
					if !quarantinable(err) || ctx.Err() != nil {
						return err
					}
					if err = quarantine(x.ID, "invalid or missing payload"); err != nil {
						return err
					}
					last = x.ID
					continue
				}
				row, u, err := mapRecord(a, r)
				stopProgress()
				if err != nil {
					if err = quarantine(x.ID, "invalid archive timestamp"); err != nil {
						return err
					}
					last = x.ID
					continue
				}
				if !exists {
					body = splitBody(&row, a.SessionID)
					rb = encodeBodyRequest(row, body.lineage, body.sk)
				}
				if !hasUsage {
					ub, err = encodeJSON(u)
					if err != nil {
						return err
					}
				}
				size = len(rb) + len(ub) + 2
				for _, b := range body.blobs {
					size += len(b)
				}

			}
			if batchFull(batchBytes, size, o.BatchBytes) {
				pending = &prepared{x.ID, rb, ub, body, size}
				break
			}
			if rb != nil {
				requests = append(requests, rb)
				requestIDs = append(requestIDs, x.ID)
				bodies = append(bodies, body)
			}
			if ub != nil {
				usage = append(usage, ub)
				usageIDs = append(usageIDs, x.ID)
			}
			batchBytes += size
			last = x.ID
		}
		bb, err := c.writeBodies(ctx, bodies)
		if err != nil && badInsert(err) {
			// Isolate a bad blob by its carrying archive row before writing any skeleton.
			for i, body := range bodies {
				_, e := c.writeBodies(ctx, []bodyItems{body})
				if e != nil {
					if !badInsert(e) {
						return e
					}
					if e = quarantine(requestIDs[i], e.Error()); e != nil {
						return e
					}
				}
			}
		} else if err != nil {
			return err
		}
		filter := func(rows [][]byte, ids []int64) ([][]byte, []int64) {
			out := [][]byte{}
			keys := []int64{}
			for i, id := range ids {
				if _, bad := s.Quarantine[id]; (bad && !o.RetryQuarantined) || batchBad[id] {
					continue
				}
				out = append(out, rows[i])
				keys = append(keys, id)
			}
			return out, keys
		}
		requests, requestIDs = filter(requests, requestIDs)
		n, b, err := c.insertSplit(ctx, "requests", requests, requestIDs, quarantine)
		if err != nil {
			return err
		}
		usage, usageIDs = filter(usage, usageIDs)
		un, ub, err := c.insertSplit(ctx, "usage", usage, usageIDs, quarantine)
		if err != nil {
			return err
		}
		if o.RetryQuarantined {
			for _, id := range usageIDs {
				if !batchBad[id] {
					delete(s.Quarantine, id)
				}
			}
		}
		n = max(n, un)

		s.LastID = last
		if err = persist(); err != nil {
			return err
		}
		if err = progress(n, bb+b+ub, false); err != nil {
			return err
		}
	}
	if o.ExternalSessions == -1 || s.ExternalDone {
		return finish()
	}
	// Session keys are immutable. A fresh state file reconciles all previously imported positions.
	query := "SELECT 0,id,'' FROM external_sessions WHERE id>? ORDER BY id"
	args := []any{s.ExternalSession}
	if o.ExternalSessions > 0 {
		query = "SELECT 0,id,'' FROM (SELECT id FROM external_sessions ORDER BY id LIMIT ?) WHERE id>? ORDER BY id"
		args = []any{o.ExternalSessions, s.ExternalSession}
	}
	if o.RetryQuarantined {
		query = "SELECT 0,id,'' FROM external_sessions WHERE id>? AND id IN (SELECT session_id FROM records WHERE 1=1" + retryFilter + ") ORDER BY id"
		args = []any{s.ExternalSession}
	}
	sessions, err := readIDs(ctx, store.DB, query, args...)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		sid := session.RequestID
		last, pos := s.NativeLastID, s.NativePosition
		for {
			ids, err := readIDs(ctx, store.DB, "SELECT r.id,r.request_id,r.started_at FROM records r WHERE r.session_id=? AND r.id>? AND r.id<=? AND r.source_format='external-native-event' "+strings.ReplaceAll(retryFilter, "id IN", "r.id IN")+" ORDER BY r.id LIMIT ?", sid, last, s.UpperID, o.Batch)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			if err = guard(); err != nil {
				return err
			}
			batchBad = map[int64]bool{}
			var rows [][]byte
			var rowIDs []int64
			batchBytes := 0
			for _, x := range ids {
				if batchBytes >= o.BatchBytes {
					break
				}
				if _, bad := s.Quarantine[x.ID]; bad && !o.RetryQuarantined {
					pos++
					last = x.ID
					continue
				}
				a, err := readRecord(x)
				if err != nil {
					if !quarantinable(err) || ctx.Err() != nil {
						return err
					}
					if err = quarantine(x.ID, "invalid or missing native payload"); err != nil {
						return err
					}
					pos++
					last = x.ID
					continue
				}
				if o.RetryQuarantined {
					if err = store.DB.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE session_id=? AND source_format='external-native-event' AND id<?", sid, x.ID).Scan(&pos); err != nil {
						return err
					}
				}
				row, err := mapExternal(a, pos, r)
				if err != nil {
					if err = quarantine(x.ID, "invalid native event or timestamp"); err != nil {
						return err
					}
					pos++
					last = x.ID
					continue
				}
				raw, err := encodeJSON(row)
				if err != nil {
					return err
				}
				if batchFull(batchBytes, len(raw)+1, o.BatchBytes) {
					break
				}
				rows = append(rows, raw)
				rowIDs = append(rowIDs, x.ID)
				batchBytes += len(raw) + 1
				pos++
				last = x.ID
			}
			n, b, err := c.insertSplit(ctx, "external_messages", rows, rowIDs, quarantine)
			if err != nil {
				return err
			}
			if o.RetryQuarantined {
				for _, id := range rowIDs {
					if !batchBad[id] {
						delete(s.Quarantine, id)
					}
				}
			}
			s.NativeLastID, s.NativePosition = last, pos
			if err = persist(); err != nil {
				return err
			}
			if err = progress(n, b, true); err != nil {
				return err
			}
		}
		s.ExternalSession = sid
		s.NativeLastID, s.NativePosition = 0, 0
		if err = persist(); err != nil {
			return err
		}
	}
	s.ExternalDone = true
	if err := persist(); err != nil {
		return err
	}
	return finish()
}

func batchFull(current, next, limit int) bool {
	return current > 0 && current+next > limit
}
