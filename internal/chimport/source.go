package chimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"cpa-session-archive/internal/archive"
)

const maxPayloadBytes = 64 << 20

type dataError string

func (e dataError) Error() string { return string(e) }
func quarantinable(err error) bool {
	var data dataError
	var syntax *json.SyntaxError
	var value *json.UnmarshalTypeError
	return errors.Is(err, sql.ErrNoRows) || errors.As(err, &data) || errors.As(err, &syntax) || errors.As(err, &value)
}

func inflate(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, dataError("corrupt gzip")
	}
	defer z.Close()
	b, err := io.ReadAll(io.LimitReader(z, maxPayloadBytes+1))
	if err != nil {
		return nil, dataError("corrupt gzip")
	}
	if len(b) > maxPayloadBytes {
		return nil, dataError("payload exceeds 64 MiB")
	}
	return b, nil
}

// Read only the retained client request and response; upstream bodies are not imported.
func readArchive(ctx context.Context, db *sql.DB, id string) (archive.Record, error) {
	var a archive.Record
	var started, done, meta, facets, original, response string
	var oldO, oldR []byte
	err := db.QueryRowContext(ctx, `SELECT request_id,coalesce(trace_id,''),session_id,coalesce(key_id,''),coalesce(source_format,''),coalesce(model,''),coalesce(requested_model,''),stream,coalesce(outcome,''),status_code,coalesce(error,''),started_at,completed_at,truncated,coalesce(metadata_json,''),coalesce(facets_json,''),coalesce(original_ref,''),coalesce(response_ref,''),original_request_gz,response_gz FROM records WHERE request_id=?`, id).Scan(&a.RequestID, &a.TraceID, &a.SessionID, &a.KeyID, &a.SourceFormat, &a.Model, &a.RequestedModel, &a.Stream, &a.Outcome, &a.StatusCode, &a.Error, &started, &done, &a.Truncated, &meta, &facets, &original, &response, &oldO, &oldR)
	if err != nil {
		return a, err
	}
	a.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return a, dataError("invalid archive timestamp")
	}
	a.CompletedAt, err = time.Parse(time.RFC3339Nano, done)
	if err != nil {
		return a, dataError("invalid archive timestamp")
	}
	for _, field := range []struct {
		raw string
		dst any
	}{{meta, &a.Metadata}, {facets, &a.Facets}} {
		if field.raw != "" {
			if err = json.Unmarshal([]byte(field.raw), field.dst); err != nil {
				return a, err
			}
		}
	}
	loaded := int64(0)
	load := func(hash string) ([]byte, error) {
		var data []byte
		var codec string
		var size int64
		err := db.QueryRowContext(ctx, `SELECT raw_size FROM blobs WHERE hash=?`, hash).Scan(&size)
		if err != nil {
			return nil, err
		}
		loaded += size
		if size < 0 || size > maxPayloadBytes || loaded > 2*maxPayloadBytes {
			return nil, dataError("blob exceeds 64 MiB")
		}
		err = db.QueryRowContext(ctx, `SELECT codec,data FROM blobs WHERE hash=?`, hash).Scan(&codec, &data)
		if err != nil {
			return nil, err
		}
		if codec == "gzip" {
			data, err = inflate(data)
		} else if codec != "" && codec != "raw" {
			return nil, dataError("unknown blob codec")
		}
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != size {
			return nil, dataError("blob length mismatch")
		}
		return data, nil
	}
	payload := func(ref string, old []byte) ([]byte, error) {
		if ref == "" {
			return inflate(old)
		}
		loaded = 0
		manifest, err := load(ref)
		if err != nil {
			return nil, err
		}
		b, err := archive.ExpandPayload(manifest, load)
		if err != nil {
			return nil, err
		}
		if len(b) > maxPayloadBytes {
			return nil, dataError("payload exceeds 64 MiB")
		}
		return b, nil
	}
	a.OriginalRequest, err = payload(original, oldO)
	if err != nil {
		return a, err
	}
	a.Response, err = payload(response, oldR)
	return a, err
}
