package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
)

func (s *server) externalSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sources, err := s.s.ExternalSources(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		http.Error(w, "source lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, sources)
}

func (s *server) externalSourceTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("source_id")
	var hash, path string
	var size int64
	err := s.s.DB.QueryRowContext(r.Context(), `SELECT sha256,bytes,json_extract(source_json,'$.path') FROM external_sources WHERE id=?`, id).Scan(&hash, &size, &path)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		http.Error(w, "source unavailable", status)
		return
	}
	name := safeFilename(filepath.Base(path)) + ".raw"
	token, expiresAt, err := s.issueTicket(exportTicket{SourceID: id, Filename: name})
	if err != nil {
		http.Error(w, "ticket unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"url": "/archive-api/v1/exports/" + token, "filename": name,
		"content_type": "application/octet-stream", "expires_at": expiresAt, "sha256": hash, "bytes": size})
}

func (s *server) streamExternalSource(w http.ResponseWriter, r *http.Request, ticket exportTicket) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var hash string
	var size int64
	if err := s.s.DB.QueryRowContext(r.Context(), `SELECT sha256,bytes FROM external_sources WHERE id=?`, ticket.SourceID).Scan(&hash, &size); err != nil {
		http.Error(w, "source unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+ticket.Filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(size))
	w.Header().Set("X-Content-SHA256", hash)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		return
	}
	if err := s.s.WriteExternalSource(r.Context(), ticket.SourceID, w); err != nil {
		log.Printf("raw source export failed id=%s: %v", ticket.SourceID, err)
	}
}
