package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const externalChunkSize = 1 << 20

func (s *Store) initExternalStore() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS external_sources(
		id TEXT PRIMARY KEY,sha256 TEXT NOT NULL,bytes INTEGER NOT NULL,source_json TEXT NOT NULL,imported_at TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS external_chunks(hash TEXT PRIMARY KEY,data BLOB NOT NULL);
	CREATE TABLE IF NOT EXISTS external_source_chunks(
		source_id TEXT NOT NULL REFERENCES external_sources(id),ordinal INTEGER NOT NULL,
		chunk_hash TEXT NOT NULL REFERENCES external_chunks(hash),PRIMARY KEY(source_id,ordinal));
	CREATE TABLE IF NOT EXISTS external_sessions(
		id TEXT PRIMARY KEY,native_id TEXT NOT NULL,metadata_json TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS external_session_sources(
		session_id TEXT NOT NULL REFERENCES external_sessions(id),source_id TEXT NOT NULL REFERENCES external_sources(id),
		PRIMARY KEY(session_id,source_id));
	CREATE TABLE IF NOT EXISTS external_events(
		session_id TEXT NOT NULL REFERENCES external_sessions(id),native_id TEXT NOT NULL,
		request_id TEXT NOT NULL UNIQUE,sha256 TEXT NOT NULL,PRIMARY KEY(session_id,native_id));`)
	return err
}

func externalHash(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func externalSessionID(source ExternalSource, nativeID string) string {
	return "external:" + externalHash(source.Host, source.Account, source.Tool, nativeID)
}

func (s *Store) ImportExternal(ctx context.Context, source ExternalSource, raw io.Reader, sessions []ExternalSession) (ExternalImportReceipt, error) {
	var receipt ExternalImportReceipt
	if source.Host == "" || source.Account == "" || source.Tool == "" || source.Path == "" || source.Format == "" || raw == nil {
		return receipt, errors.New("external source requires host, account, tool, path, format and raw reader")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	receipt.Source, receipt.Replayed, err = streamExternalSource(ctx, tx, source, raw)
	if err != nil {
		return ExternalImportReceipt{}, err
	}
	for _, session := range sessions {
		id, added, err := s.importExternalSession(ctx, tx, receipt.Source, session)
		if err != nil {
			return ExternalImportReceipt{}, err
		}
		receipt.SessionIDs = append(receipt.SessionIDs, id)
		receipt.Events += len(session.Events)
		receipt.ImportedEvents += added
	}
	receipt.Sessions = len(receipt.SessionIDs)
	if err = tx.Commit(); err != nil {
		return ExternalImportReceipt{}, err
	}
	return receipt, nil
}

func streamExternalSource(ctx context.Context, tx *sql.Tx, source ExternalSource, raw io.Reader) (ExternalSourceInfo, bool, error) {
	info := ExternalSourceInfo{Source: source, ImportedAt: time.Now().UTC()}
	hash := sha256.New()
	var chunks []string
	buffer := make([]byte, externalChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return info, false, err
		}
		n, err := io.ReadFull(raw, buffer)
		if n > 0 {
			chunk := buffer[:n]
			hash.Write(chunk)
			info.Bytes += int64(n)
			sum := sha256.Sum256(chunk)
			chunkID := hex.EncodeToString(sum[:])
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_chunks(hash,data) VALUES(?,?)`, chunkID, chunk); err != nil {
				return info, false, err
			}
			chunks = append(chunks, chunkID)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return info, false, err
		}
	}
	info.SHA256 = hex.EncodeToString(hash.Sum(nil))
	info.ID = "external-source:" + externalHash(source.Host, source.Account, source.Tool, source.Path, source.Format, info.SHA256)
	var existingJSON, importedAt string
	err := tx.QueryRowContext(ctx, `SELECT source_json,imported_at FROM external_sources WHERE id=?`, info.ID).Scan(&existingJSON, &importedAt)
	if err == nil {
		if err = json.Unmarshal([]byte(existingJSON), &info.Source); err != nil {
			return info, false, err
		}
		info.ImportedAt, err = time.Parse(time.RFC3339Nano, importedAt)
		return info, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return info, false, err
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return info, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO external_sources(id,sha256,bytes,source_json,imported_at) VALUES(?,?,?,?,?)`, info.ID, info.SHA256, info.Bytes, string(sourceJSON), info.ImportedAt.Format(time.RFC3339Nano)); err != nil {
		return info, false, err
	}
	for i, chunk := range chunks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO external_source_chunks(source_id,ordinal,chunk_hash) VALUES(?,?,?)`, info.ID, i, chunk); err != nil {
			return info, false, err
		}
	}
	return info, false, nil
}

func (s *Store) ExternalSources(ctx context.Context, sessionID string) ([]ExternalSourceInfo, error) {
	query := `SELECT s.id,s.sha256,s.bytes,s.source_json,s.imported_at FROM external_sources s`
	args := []any{}
	if sessionID != "" {
		query += ` JOIN external_session_sources x ON x.source_id=s.id WHERE x.session_id=?`
		args = append(args, sessionID)
	}
	rows, err := s.DB.QueryContext(ctx, query+` ORDER BY s.imported_at,s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExternalSourceInfo{}
	for rows.Next() {
		var info ExternalSourceInfo
		var sourceJSON, importedAt string
		if err = rows.Scan(&info.ID, &info.SHA256, &info.Bytes, &sourceJSON, &importedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(sourceJSON), &info.Source); err != nil {
			return nil, err
		}
		if info.ImportedAt, err = time.Parse(time.RFC3339Nano, importedAt); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

func (s *Store) WriteExternalSource(ctx context.Context, sourceID string, dst io.Writer) error {
	var expected string
	var size int64
	if err := s.DB.QueryRowContext(ctx, `SELECT sha256,bytes FROM external_sources WHERE id=?`, sourceID).Scan(&expected, &size); err != nil {
		return err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT c.data FROM external_source_chunks s JOIN external_chunks c ON c.hash=s.chunk_hash WHERE s.source_id=? ORDER BY s.ordinal`, sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	hash := sha256.New()
	var written int64
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return err
		}
		n, err := io.Copy(io.MultiWriter(dst, hash), bytes.NewReader(data))
		if err != nil {
			return err
		}
		written += n
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("external source %s failed integrity check", sourceID)
	}
	return nil
}
