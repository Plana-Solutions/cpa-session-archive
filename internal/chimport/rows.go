package chimport

// Row shapes follow cpa-clickhouse-sink v0.1.0-plana.1 capture.go.
type requestRow struct {
	RequestID       string            `json:"request_id"`
	TraceID         string            `json:"trace_id"`
	StartedAt       string            `json:"started_at"`
	CompletedAt     string            `json:"completed_at"`
	SourceFormat    string            `json:"source_format"`
	Model           string            `json:"model"`
	RequestedModel  string            `json:"requested_model"`
	Path            string            `json:"path"`
	Stream          bool              `json:"stream"`
	Outcome         string            `json:"outcome"`
	StatusCode      int               `json:"status_code"`
	Error           string            `json:"error"`
	RequestHeaders  map[string]string `json:"request_headers"`
	RequestBody     string            `json:"request_body"`
	ResponseHeaders map[string]string `json:"response_headers"`
	ResponseBody    string            `json:"response_body"`
	ResponseChunks  []string          `json:"response_chunks"`
	RedactionCount  int               `json:"redaction_count"`
	RedactionRules  []string          `json:"redaction_rules"`
}

type usageRow struct {
	RequestID            string            `json:"request_id"`
	TraceID              string            `json:"trace_id"`
	SelectionID          string            `json:"selection_id"`
	RequestedAt          string            `json:"requested_at"`
	Provider             string            `json:"provider"`
	ExecutorType         string            `json:"executor_type"`
	BaseURL              string            `json:"base_url"`
	Model                string            `json:"model"`
	Alias                string            `json:"alias"`
	ResponseModel        string            `json:"response_model"`
	APIKeyHash           string            `json:"api_key_hash"`
	AccessProvider       string            `json:"access_provider"`
	SessionID            string            `json:"session_id"`
	ParentSessionID      string            `json:"parent_session_id"`
	AuthID               string            `json:"auth_id"`
	AuthIndex            string            `json:"auth_index"`
	AuthType             string            `json:"auth_type"`
	Source               string            `json:"source"`
	ReasoningEffort      string            `json:"reasoning_effort"`
	ServiceTier          string            `json:"service_tier"`
	EffectiveServiceTier string            `json:"effective_service_tier"`
	ResponseServiceTier  string            `json:"response_service_tier"`
	Stream               bool              `json:"stream"`
	Generate             bool              `json:"generate"`
	LatencyMS            int64             `json:"latency_ms"`
	TTFTMS               int64             `json:"ttft_ms"`
	Failed               bool              `json:"failed"`
	FailureStatus        int               `json:"failure_status"`
	FailureBody          string            `json:"failure_body"`
	InputTokens          int64             `json:"input_tokens"`
	OutputTokens         int64             `json:"output_tokens"`
	ReasoningTokens      int64             `json:"reasoning_tokens"`
	CachedTokens         int64             `json:"cached_tokens"`
	CacheReadTokens      int64             `json:"cache_read_tokens"`
	CacheCreationTokens  int64             `json:"cache_creation_tokens"`
	TotalTokens          int64             `json:"total_tokens"`
	ResponseHeaders      map[string]string `json:"response_headers"`
}
