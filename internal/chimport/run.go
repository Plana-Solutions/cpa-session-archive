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
	Log                                                 io.Writer
}
type checkpoint struct {
	Identity        string `json:"identity"`
	LastID, UpperID int64
	ExternalSession string
	ExternalDone    bool
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
	s := checkpoint{Identity: hex.EncodeToString(sum[:])}
	b, err := os.ReadFile(o.State)
	if err == nil {
		var old checkpoint
		if err = json.Unmarshal(b, &old); err != nil {
			return s, err
		}
		if old.Identity != s.Identity {
			return s, errors.New("state belongs to a different source, target or sample")
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
	return s, save(o.State, s)
}
func readIDs(ctx context.Context, db *sql.DB, q string, args ...any) ([]item, error) {
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
	s, err := loadState(o, store.DB)
	if err != nil {
		return err
	}
	c := newClient(o.URL, o.User, o.Password)
	start := time.Now()
	batchStart := start
	var rowsN, bytesN, skipped int64
	progress := func(n int, b int64) error {
		rowsN += int64(n)
		bytesN += b
		if o.Rate > 0 {
			delay := time.Duration(float64(n)/o.Rate*float64(time.Second)) - time.Since(batchStart)
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		elapsed := time.Since(start).Seconds()
		if o.Log != nil {
			fmt.Fprintf(o.Log, "rows=%d skipped=%d last_id=%d rows/s=%.2f MB/s=%.2f elapsed=%s\n", rowsN, skipped, s.LastID, float64(rowsN)/elapsed, float64(bytesN)/1e6/elapsed, time.Since(start).Round(time.Millisecond))
		}
		batchStart = time.Now()
		return nil
	}
	for {
		ids, err := readIDs(ctx, store.DB, "SELECT id,request_id FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' ORDER BY id LIMIT ?", s.LastID, s.UpperID, o.Batch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
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
		requests := []requestRow{}
		usage := []usageRow{}
		for _, x := range ids {
			imported, exists := present[x.RequestID]
			_, hasUsage := used["archive-v1:"+x.RequestID]
			if exists && (!imported || hasUsage) {
				skipped++
				continue
			}
			a, err := store.Request(ctx, x.RequestID)
			if err != nil {
				return fmt.Errorf("read record id %d: %w", x.ID, err)
			}
			row, u, err := mapRecord(a, r)
			if err != nil {
				return fmt.Errorf("map record id %d: %w", x.ID, err)
			}
			if !exists {
				requests = append(requests, row)
			}
			if !hasUsage {
				usage = append(usage, u)
			}
		}
		b, err := c.insert(ctx, "requests", requests)
		if err != nil {
			return err
		}
		ub, err := c.insert(ctx, "usage", usage)
		if err != nil {
			return err
		}
		s.LastID = ids[len(ids)-1].ID
		if err = save(o.State, s); err != nil {
			return err
		}
		if err = progress(len(ids), b+ub); err != nil {
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
	query := "SELECT id FROM external_sessions WHERE id>? ORDER BY id"
	args := []any{s.ExternalSession}
	if o.ExternalSessions > 0 {
		query = "SELECT id FROM (SELECT id FROM external_sessions ORDER BY id LIMIT ?) WHERE id>? ORDER BY id"
		args = []any{o.ExternalSessions, s.ExternalSession}
	}
	rr, err := store.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	sessions := []string{}
	for rr.Next() {
		var id string
		if err = rr.Scan(&id); err != nil {
			rr.Close()
			return err
		}
		sessions = append(sessions, id)
	}
	err = rr.Err()
	rr.Close()
	if err != nil {
		return err
	}
	for _, sid := range sessions {
		var last int64
		var pos uint32
		for {
			ids, err := readIDs(ctx, store.DB, "SELECT r.id,r.request_id FROM records r WHERE r.session_id=? AND r.id>? AND r.source_format='external-native-event' ORDER BY r.id LIMIT ?", sid, last, o.Batch)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			rows := []externalRow{}
			for _, x := range ids {
				a, err := store.Request(ctx, x.RequestID)
				if err != nil {
					return err
				}
				row, err := mapExternal(a, pos, r)
				if err != nil {
					return err
				}
				rows = append(rows, row)
				pos++
				last = x.ID
			}
			b, err := c.insert(ctx, "external_messages", rows)
			if err != nil {
				return err
			}
			if err = progress(len(rows), b); err != nil {
				return err
			}
		}
		s.ExternalSession = sid
		if err = save(o.State, s); err != nil {
			return err
		}
	}
	s.ExternalDone = true
	return save(o.State, s)
}
