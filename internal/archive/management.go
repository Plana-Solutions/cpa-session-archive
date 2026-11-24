package archive

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const pluginID = "cpa-session-archive"

//go:embed web/index.html
var managementHTML []byte

//go:embed web/external.js
var managementExternalJS []byte

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}
type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func (p *Plugin) managementRegistration() map[string]any {
	base := "/plugins/" + pluginID
	return map[string]any{"Routes": []map[string]string{{"Method": "GET", "Path": base + "/stats"}, {"Method": "GET", "Path": base + "/insights"}, {"Method": "GET", "Path": base + "/facets"}, {"Method": "GET", "Path": base + "/sessions"}, {"Method": "GET", "Path": base + "/cancel-search"}, {"Method": "GET", "Path": base + "/turns"}, {"Method": "GET", "Path": base + "/turn-text"}, {"Method": "GET", "Path": base + "/requests"}, {"Method": "GET", "Path": base + "/request-context"}, {"Method": "GET", "Path": base + "/request-view"}, {"Method": "GET", "Path": base + "/export"}, {"Method": "GET", "Path": base + "/external-sources"}, {"Method": "GET", "Path": base + "/external-source"}}, "Resources": []map[string]string{{"Path": "/index.html", "Menu": "Session Archive", "Description": "Fleethouse insights, session browser, and conversation reader backed by ClickHouse."}, {"Path": "/external.js"}}}
}
func (p *Plugin) handleManagement(raw []byte) ([]byte, error) {
	return p.handleManagementContext(context.Background(), raw)
}
func (p *Plugin) handleManagementContext(ctx context.Context, raw []byte) ([]byte, error) {
	var r managementRequest
	if e := json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	path := strings.TrimRight(r.Path, "/")
	resource := "/v0/resource/plugins/" + pluginID
	if r.Method == http.MethodGet && strings.HasPrefix(path, resource) {
		status := http.StatusNotFound
		body := []byte("not found")
		contentType := "text/html; charset=utf-8"
		switch strings.TrimPrefix(path, resource) {
		case "/index.html":
			status = http.StatusOK
			body = managementHTML
		case "/external.js":
			status = http.StatusOK
			body = managementExternalJS
			contentType = "text/javascript; charset=utf-8"
		}
		return ok(managementResponse{
			StatusCode: status,
			Headers: http.Header{
				"Content-Type":  []string{contentType},
				"Cache-Control": []string{"no-store, max-age=0"},
			},
			Body: body,
		})
	}

	base := "/v0/management/plugins/" + pluginID + "/"
	if r.Method != http.MethodGet || !strings.HasPrefix(path, base) {
		return managementJSON(404, map[string]string{"error": "not found"})
	}
	p.mu.RLock()
	reader := p.reader
	p.mu.RUnlock()
	if reader == nil || reader.config.URL == "" {
		return managementJSON(503, map[string]string{"error": "Configure clickhouse-url to read the archive"})
	}
	value, err := reader.readContext(ctx, strings.TrimPrefix(path, base), r.Query)
	if err != nil {
		status := 502
		var apiErr *archiveReadError
		if errors.As(err, &apiErr) {
			status = apiErr.status
		}
		return managementJSON(status, map[string]string{"error": err.Error()})
	}
	return managementJSON(200, value)
}
func managementJSON(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return ok(managementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: body})
}
