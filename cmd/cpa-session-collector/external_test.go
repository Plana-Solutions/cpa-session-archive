package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestExternalSourceTicketStreamsExactBytes(t *testing.T) {
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	raw := bytes.Repeat([]byte("original raw session\n"), 70000)
	receipt, err := store.ImportExternal(context.Background(), archive.ExternalSource{
		Host: "fixture", Account: "review", Tool: "codex", Path: "/sessions/log.jsonl", Format: "raw",
	}, bytes.NewReader(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{s: store, tickets: map[string]exportTicket{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/external-sources", s.externalSources)
	mux.HandleFunc("/v1/external-source-ticket", s.externalSourceTicket)
	mux.HandleFunc("/archive-api/v1/exports/", s.ticketedExport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/external-sources")
	if err != nil {
		t.Fatal(err)
	}
	var sources []archive.ExternalSourceInfo
	err = json.NewDecoder(res.Body).Decode(&sources)
	res.Body.Close()
	if err != nil || len(sources) != 1 || sources[0].ID != receipt.Source.ID {
		t.Fatalf("source inventory: %v", err)
	}
	res, err = http.Get(srv.URL + "/v1/external-source-ticket?source_id=" + url.QueryEscape(receipt.Source.ID))
	if err != nil {
		t.Fatal(err)
	}
	var ticket struct {
		URL, SHA256 string
		Bytes       int64
	}
	err = json.NewDecoder(res.Body).Decode(&ticket)
	res.Body.Close()
	if err != nil || ticket.URL == "" || ticket.SHA256 != receipt.Source.SHA256 || ticket.Bytes != int64(len(raw)) {
		t.Fatalf("source ticket: %v", err)
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, _ := http.NewRequest(method, srv.URL+ticket.URL, nil)
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || res.Header.Get("X-Content-SHA256") != receipt.Source.SHA256 || res.ContentLength != int64(len(raw)) {
			t.Fatalf("raw stream headers/content: %v", err)
		}
		if method == http.MethodGet {
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != ticket.SHA256 || !bytes.Equal(got, raw) {
				t.Fatal("raw download does not match original")
			}
		}
	}
	s.ticketMu.Lock()
	for token, item := range s.tickets {
		item.ExpiresAt = time.Now().Add(-time.Second)
		s.tickets[token] = item
	}
	s.ticketMu.Unlock()
	res, err = http.Get(srv.URL + ticket.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatal("expired ticket remains usable")
	}
}
