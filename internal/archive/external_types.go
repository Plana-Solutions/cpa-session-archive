package archive

import (
	"encoding/json"
	"time"
)

type ExternalSource struct {
	Host       string    `json:"host"`
	Account    string    `json:"account"`
	Tool       string    `json:"tool"`
	Path       string    `json:"path"`
	Format     string    `json:"format"`
	ModifiedAt time.Time `json:"modified_at"`
}

type ExternalEvent struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parent_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Role      string          `json:"role"`
	Text      string          `json:"text,omitempty"`
	Name      string          `json:"name,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type ExternalSession struct {
	ID          string          `json:"id"`
	ParentID    string          `json:"parent_id,omitempty"`
	Title       string          `json:"title,omitempty"`
	ProjectPath string          `json:"project_path,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
	Events      []ExternalEvent `json:"events"`
}

type ExternalSourceInfo struct {
	ID         string         `json:"id"`
	SHA256     string         `json:"sha256"`
	Bytes      int64          `json:"bytes"`
	Source     ExternalSource `json:"source"`
	ImportedAt time.Time      `json:"imported_at"`
}

type ExternalImportReceipt struct {
	Source         ExternalSourceInfo `json:"source"`
	SessionIDs     []string           `json:"session_ids"`
	Sessions       int                `json:"sessions"`
	Events         int                `json:"events"`
	ImportedEvents int                `json:"imported_events"`
	Replayed       bool               `json:"replayed"`
}
