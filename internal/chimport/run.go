package chimport

import (
	"context"
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
}
type checkpoint struct {
	Identity        string `json:"identity"`
	LastID, UpperID int64
	FirstID         int64
	ExternalSession string
	ExternalDone    bool
	NativeLastID    int64
	NativePosition  uint32
	NoModelSkipped  int64
	Quarantine      map[int64]string
}
type item struct {
	ID        int64
	RequestID string
}

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
	sum := sha256.Sum256([]byte(fmt.Sprintf("archive-v1|%s|%s|%s|%d|%d", abs, o.URL, o.User, o.Newest, o.ExternalSessions)))
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
		if err = rows.Scan(&x.ID, &x.RequestID); err != nil {
			return nil, err
		}
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
		o.DiskCheckBatches = 100
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
	c := newClient(o.URL, o.User, o.Password)
	start := time.Now()
	batchStart := start
	var rowsN, nativeN, bytesN, skipped int64
	batches := 0
	if err = c.diskStatus(ctx, o.MinFreeBytes, o.Log); err != nil {
		return err
	}
	var noModel int64
	err = retry(ctx, func() error {
		return store.DB.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' AND COALESCE(model,'')='' AND COALESCE(requested_model,'')=''", s.FirstID, s.UpperID).Scan(&noModel)
	})
	if err != nil {
		return err
	}
	s.NoModelSkipped = noModel
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
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		if o.Log != nil {
			elapsed := time.Since(start).Seconds()
			fmt.Fprintf(o.Log, "proxy_rows=%d native_events=%d skipped_existing=%d skipped_no_model=%d quarantined=%d last_id=%d rows/s=%.2f MB/s=%.2f elapsed=%s\n", rowsN, nativeN, skipped, s.NoModelSkipped, len(s.Quarantine), s.LastID, float64(rowsN+nativeN)/elapsed, float64(bytesN)/1e6/elapsed, time.Since(start).Round(time.Millisecond))
		}
		batchStart = time.Now()
		batches++
		return nil
	}
	guard := func() error {
		if batches > 0 && batches%o.DiskCheckBatches == 0 {
			return c.diskStatus(ctx, o.MinFreeBytes, o.Log)
		}
		return ctx.Err()
	}
	readRecord := func(x item) (a archive.Record, err error) {
		err = retry(ctx, func() error { var e error; a, e = store.Request(ctx, x.RequestID); return e })
		return
	}
	for {
		if err = guard(); err != nil {
			return err
		}
		ids, err := readIDs(ctx, store.DB, "SELECT id,request_id FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' AND (COALESCE(model,'')<>'' OR COALESCE(requested_model,'')<>'') ORDER BY id LIMIT ?", s.LastID, s.UpperID, o.Batch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			s.LastID = s.UpperID
			if err = save(o.State, s); err != nil {
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
		present, err := c.existing(ctx, "requests", keys)
		if err != nil {
			return err
		}
		used, err := c.existing(ctx, "usage", ukeys)
		if err != nil {
			return err
		}
		var requests, usage []json.RawMessage
		batchBytes, processed := 0, 0
		last := s.LastID
		for _, x := range ids {
			imported, exists := present[x.RequestID]
			_, hasUsage := used["archive-v1:"+x.RequestID]
			if _, bad := s.Quarantine[x.ID]; bad {
				processed++
				last = x.ID
				continue
			}
			if exists && (!imported || hasUsage) {
				skipped++
				processed++
				last = x.ID
				continue
			}
			a, err := readRecord(x)
			if err != nil {
				if sqliteBusy(err) || ctx.Err() != nil {
					return err
				}
				s.Quarantine[x.ID] = "payload read failed"
				processed++
				last = x.ID
				continue
			}
			row, u, err := mapRecord(a, r)
			if err != nil {
				s.Quarantine[x.ID] = "invalid archive timestamp"
				processed++
				last = x.ID
				continue
			}
			var rb, ub json.RawMessage
			if !exists {
				rb, err = json.Marshal(row)
				if err != nil {
					return err
				}
			}
			if !hasUsage {
				ub, err = json.Marshal(u)
				if err != nil {
					return err
				}
			}
			size := len(rb) + len(ub) + 2
			if batchFull(batchBytes, size, o.BatchBytes) {
				break
			}
			if rb != nil {
				requests = append(requests, rb)
			}
			if ub != nil {
				usage = append(usage, ub)
			}
			batchBytes += size
			processed++
			last = x.ID
		}
		b, err := c.insert(ctx, "requests", requests)
		if err != nil {
			return err
		}
		ub, err := c.insert(ctx, "usage", usage)
		if err != nil {
			return err
		}
		s.LastID = last
		if err = save(o.State, s); err != nil {
			return err
		}
		if err = progress(processed, b+ub, false); err != nil {
			return err
		}
	}
	if o.ExternalSessions == -1 || s.ExternalDone {
		return nil
	}
	if _, err = c.call(ctx, externalDDL, nil, nil, false); err != nil {
		return err
	}
	// Session keys are immutable. A fresh state file reconciles all previously imported positions.
	query := "SELECT 0,id FROM external_sessions WHERE id>? ORDER BY id"
	args := []any{s.ExternalSession}
	if o.ExternalSessions > 0 {
		query = "SELECT 0,id FROM (SELECT id FROM external_sessions ORDER BY id LIMIT ?) WHERE id>? ORDER BY id"
		args = []any{o.ExternalSessions, s.ExternalSession}
	}
	sessions, err := readIDs(ctx, store.DB, query, args...)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		sid := session.RequestID
		last, pos := s.NativeLastID, s.NativePosition
		for {
			ids, err := readIDs(ctx, store.DB, "SELECT r.id,r.request_id FROM records r WHERE r.session_id=? AND r.id>? AND r.id<=? AND r.source_format='external-native-event' ORDER BY r.id LIMIT ?", sid, last, s.UpperID, o.Batch)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			if err = guard(); err != nil {
				return err
			}
			var rows []json.RawMessage
			batchBytes, processed := 0, 0
			for _, x := range ids {
				if _, bad := s.Quarantine[x.ID]; bad {
					pos++
					last = x.ID
					processed++
					continue
				}
				a, err := readRecord(x)
				if err != nil {
					if sqliteBusy(err) || ctx.Err() != nil {
						return err
					}
					s.Quarantine[x.ID] = "native payload read failed"
					pos++
					last = x.ID
					processed++
					continue
				}
				row, err := mapExternal(a, pos, r)
				if err != nil {
					s.Quarantine[x.ID] = "invalid native event or timestamp"
					pos++
					last = x.ID
					processed++
					continue
				}
				raw, err := json.Marshal(row)
				if err != nil {
					return err
				}
				if batchFull(batchBytes, len(raw)+1, o.BatchBytes) {
					break
				}
				rows = append(rows, raw)
				batchBytes += len(raw) + 1
				pos++
				last = x.ID
				processed++
			}
			b, err := c.insert(ctx, "external_messages", rows)
			if err != nil {
				return err
			}
			s.NativeLastID, s.NativePosition = last, pos
			if err = save(o.State, s); err != nil {
				return err
			}
			if err = progress(processed, b, true); err != nil {
				return err
			}
		}
		s.ExternalSession = sid
		s.NativeLastID, s.NativePosition = 0, 0
		if err = save(o.State, s); err != nil {
			return err
		}
	}
	s.ExternalDone = true
	return save(o.State, s)
}

func batchFull(current, next, limit int) bool {
	return current > 0 && current+next > limit
}
