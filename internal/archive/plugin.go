package archive

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginConfig struct {
	URL      string `yaml:"clickhouse-url"`
	Database string `yaml:"clickhouse-database"`
	User     string `yaml:"clickhouse-user"`
	Password string `yaml:"clickhouse-password"`
}
type lifecycle struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type Plugin struct {
	mu     sync.RWMutex
	reader *clickhouseReader
}
type envelope struct {
	OK     bool `json:"ok"`
	Result any  `json:"result,omitempty"`
	Error  any  `json:"error,omitempty"`
}

func NewPlugin() *Plugin       { return &Plugin{} }
func ok(v any) ([]byte, error) { return json.Marshal(envelope{OK: true, Result: v}) }
func (p *Plugin) Handle(method string, raw []byte) ([]byte, error) {
	return p.HandleContext(context.Background(), method, raw)
}

func (p *Plugin) HandleContext(ctx context.Context, method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return p.configure(raw)
	case "management.register":
		return ok(p.managementRegistration())
	case "management.handle":
		return p.handleManagementContext(ctx, raw)
	default:
		return nil, errors.New("unknown method")
	}
}
func (p *Plugin) configure(raw []byte) ([]byte, error) {
	var l lifecycle
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	c := pluginConfig{Database: "cliproxy", User: "inference_reader"}
	if err := yaml.Unmarshal(l.ConfigYAML, &c); err != nil {
		return nil, errors.New("invalid archive configuration")
	}
	if c.Password == "" {
		c.Password = os.Getenv("CLICKHOUSE_READER_PASSWORD")
	}
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("clickhouse-url must be an HTTP endpoint without credentials or query parameters")
		}
	}
	if c.Database == "" || c.User == "" {
		return nil, errors.New("ClickHouse database and user are required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	reader := &clickhouseReader{config: c, client: &http.Client{Timeout: 60 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.mu.Lock()
	old := p.reader
	p.reader = reader
	p.mu.Unlock()
	if old != nil {
		old.client.CloseIdleConnections()
	}
	fields := []map[string]any{
		{"Name": "clickhouse-url", "Type": "string", "Default": "", "Description": "ClickHouse HTTP endpoint. Required for archive reads."},
		{"Name": "clickhouse-database", "Type": "string", "Default": "cliproxy"},
		{"Name": "clickhouse-user", "Type": "string", "Default": "inference_reader"},
		{"Name": "clickhouse-password", "Type": "string", "Default": "", "Description": "Falls back to CLICKHOUSE_READER_PASSWORD."},
	}
	return ok(map[string]any{"schema_version": 7, "metadata": map[string]any{"Name": pluginID, "Version": Version, "Author": "OneTwo / Plana-Solutions", "GitHubRepository": "https://github.com/Plana-Solutions/cpa-session-archive", "ConfigFields": fields}, "capabilities": map[string]bool{"management_api": true}})
}
func (p *Plugin) Shutdown() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.reader != nil {
		p.reader.client.CloseIdleConnections()
	}
}
