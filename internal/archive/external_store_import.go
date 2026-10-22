package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

func putExternalPayload(tx *sql.Tx, raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	blob := newBlob(raw, "application/json")
	if err := putBlob(tx, blob); err != nil {
		return "", err
	}
	manifest, _ := json.Marshal(blobRef{Blob: blob.Hash, Encoding: "raw", Size: len(raw), MediaType: blob.MediaType})
	mb := newBlob(manifest, "application/vnd.cpa.archive-manifest+json")
	return mb.Hash, putBlob(tx, mb)
}

func (s *Store) importExternalSession(ctx context.Context, tx *sql.Tx, source ExternalSourceInfo, session ExternalSession) (string, int, error) {
	if session.ID == "" {
		return "", 0, errors.New("external session requires native ID")
	}
	id := externalSessionID(source.Source, session.ID)
	metadata := session
	metadata.Events = nil
	sessionJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO external_sessions(id,native_id,metadata_json) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET metadata_json=excluded.metadata_json`, id, session.ID, string(sessionJSON)); err != nil {
		return "", 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_session_sources(session_id,source_id) VALUES(?,?)`, id, source.ID); err != nil {
		return "", 0, err
	}
	added := 0
	batch := make([]Record, 0, 128)
	flush := func() error {
		if err := s.putBatchTx(tx, batch, putExternalPayload); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for ordinal, event := range session.Events {
		payload, err := json.Marshal(struct {
			Event ExternalEvent `json:"external_event"`
		}{event})
		if err != nil {
			return "", 0, fmt.Errorf("external event %q: %w", event.ID, err)
		}
		eventID := event.ID
		if eventID == "" {
			eventID = "ordinal:" + strconv.Itoa(ordinal)
		}
		sum := sha256.Sum256(payload)
		eventSHA := hex.EncodeToString(sum[:])
		var existingSHA string
		err = tx.QueryRowContext(ctx, `SELECT sha256 FROM external_events WHERE session_id=? AND native_id=?`, id, eventID).Scan(&existingSHA)
		if err == nil {
			if existingSHA != eventSHA {
				return "", 0, fmt.Errorf("external event content conflict for session %q event %q", session.ID, eventID)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", 0, err
		}
		requestID := "external-event:" + externalHash(id, eventID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO external_events(session_id,native_id,request_id,sha256) VALUES(?,?,?,?)`, id, eventID, requestID, eventSHA); err != nil {
			return "", 0, err
		}
		record := externalEventRecord(source, session, event, id, requestID, payload)
		batch = append(batch, record)
		if len(batch) == cap(batch) {
			if err = flush(); err != nil {
				return "", 0, err
			}
		}
		added++
	}
	if len(batch) > 0 {
		if err = flush(); err != nil {
			return "", 0, err
		}
	}
	return id, added, nil
}

func externalEventRecord(source ExternalSourceInfo, session ExternalSession, event ExternalEvent, sessionID, requestID string, payload []byte) Record {
	facets := map[string][]string{
		"source.kind":       {"external-local-history"},
		"source.tool":       {source.Source.Tool},
		"source.account":    {source.Source.Account},
		"source.host":       {source.Source.Host},
		"source.session_id": {session.ID},
		"source.artifact":   {source.Source.Path},
		"source.format":     {source.Source.Format},
		"source.role":       {event.Role},
		"session.id":        {sessionID},
		"request.kind":      {"external-event"},
		"thread.source":     {"external-local-history"},
		"client":            {source.Source.Tool},
	}
	projectName := ""
	if session.ProjectPath != "" {
		projectName = filepath.Base(session.ProjectPath)
		facets["project.path"] = []string{session.ProjectPath}
		facets["project.name"] = []string{projectName}
	}
	if event.Name != "" {
		facets["tool.name"] = []string{event.Name}
	}
	return Record{
		RequestID: requestID, SessionID: sessionID, SourceFormat: "external-native-event",
		Outcome: "imported", StatusCode: 0, StartedAt: event.Timestamp, CompletedAt: event.Timestamp,
		Summary: event.Text, ResponsePreview: event.Text, ProjectPath: session.ProjectPath,
		ProjectName: projectName, OriginalRequest: payload, Facets: facets,
		Metadata: map[string]any{"external_source": source, "external_session": map[string]any{
			"id": session.ID, "parent_id": session.ParentID, "title": session.Title,
			"project_path": session.ProjectPath, "started_at": session.StartedAt, "completed_at": session.CompletedAt,
		}},
	}
}
