package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

func extractResponsePreview(body []byte) string {
	if terminal, ok := terminalSSEPayload(body); ok {
		body = terminal
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var candidates []string
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			typeName := strings.ToLower(fmt.Sprint(item["type"]))
			if typeName == "function_call" || typeName == "custom_tool_call" {
				if name := strings.TrimSpace(fmt.Sprint(item["name"])); name != "" && name != "<nil>" {
					candidates = append(candidates, "工具调用："+name)
				}
			}
			if role := strings.ToLower(fmt.Sprint(item["role"])); role == "assistant" || strings.Contains(typeName, "output_text") {
				for _, key := range []string{"text", "output_text", "content"} {
					walkPreviewText(item[key], &candidates)
				}
			}
			for _, key := range []string{"response", "output", "content"} {
				walk(item[key])
			}
		}
	}
	walk(root)
	if len(candidates) == 0 {
		return ""
	}
	return compactPreview(candidates[len(candidates)-1], 240)
}

func walkPreviewText(value any, out *[]string) {
	switch item := value.(type) {
	case string:
		if text := strings.TrimSpace(item); text != "" {
			*out = append(*out, text)
		}
	case []any:
		for _, child := range item {
			walkPreviewText(child, out)
		}
	case map[string]any:
		for _, key := range []string{"text", "output_text", "content"} {
			walkPreviewText(item[key], out)
		}
	}
}

func compactPreview(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}
func addCompletionFacets(rec *Record) {
	if rec.Facets == nil {
		rec.Facets = map[string][]string{}
	}
	add := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			rec.Facets[k] = []string{v}
		}
	}
	add("session.id", rec.SessionID)
	add("model.requested", rec.RequestedModel)
	add("model.resolved", rec.Model)
	add("source.format", rec.SourceFormat)
	add("outcome", rec.Outcome)
	add("key.id", rec.KeyID)
	if rec.Metadata != nil {
		add("provider.target", findString(rec.Metadata, "target_provider"))
		add("model.target", findString(rec.Metadata, "target_model"))
		add("auth.group", findString(rec.Metadata, "group"))
		add("auth.id", findString(rec.Metadata, "selected_auth_id"))
		// caller_scope is a host-salted compatibility identity. Once CPA has
		// supplied a native Key identity, indexing both would make one caller
		// appear twice and the salted value cannot be resolved to a CPAMP alias.
		callerScope := findString(rec.Metadata, "caller_scope")
		if callerScope == rec.KeyID {
			add("caller.scope", callerScope)
		}
		add("request.path", findString(rec.Metadata, "request_path"))
	}
	rec.Facets["stream"] = []string{fmt.Sprint(rec.Stream)}
	if rec.StatusCode > 0 {
		rec.Facets["status.code"] = []string{fmt.Sprint(rec.StatusCode)}
	}
}
func limit(b []byte, n int, t *bool) []byte {
	if len(b) > n {
		*t = true
		b = b[:n]
	}
	return append([]byte(nil), b...)
}
func firstJSON(bs ...any) string {
	var key string
	var arr [][]byte
	for _, x := range bs {
		switch v := x.(type) {
		case []byte:
			arr = append(arr, v)
		case string:
			key = v
		}
	}
	for _, b := range arr {
		if v := gjson.GetBytes(b, key).String(); v != "" {
			return v
		}
	}
	return ""
}
func responseID(b []byte) string {
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if v := gjson.GetBytes(line, "response.id").String(); v != "" {
			return v
		}
		if v := gjson.GetBytes(line, "id").String(); strings.HasPrefix(v, "resp_") {
			return v
		}
	}
	return ""
}
func sessionID(rec *Record, m map[string]any, bs ...[]byte) string {
	// Codex Desktop creates a new execution_session_id for retries and remote
	// executions inside one visible task.  The thread identity carried by the
	// client is the durable archive boundary; execution IDs remain metadata.
	if rec != nil {
		if v := firstNonEmpty(rec.ThreadID, stableWindowID(rec.WindowID)); v != "" {
			return v
		}
	}
	for _, b := range bs {
		for _, k := range []string{"thread_id", "session_id", "client_metadata.thread_id", "client_metadata.session_id", "prompt_cache_key", "client_metadata.x-codex-window-id"} {
			if v := gjson.GetBytes(b, k).String(); v != "" {
				return firstNonEmpty(stableWindowID(v), v)
			}
		}
		u := gjson.GetBytes(b, "metadata.user_id").String()
		if strings.HasPrefix(u, "{") {
			if v := gjson.Get(u, "session_id").String(); v != "" {
				return v
			}
		}
	}
	if v := findString(m, "thread_id", "session_id"); v != "" {
		return v
	}
	if v := findString(m, "execution_session_id", "derived_session_id"); v != "" {
		return v
	}
	return "request:" + findString(m, "request_id")
}

func stableWindowID(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ':'); i > 0 {
		return v[:i]
	}
	return v
}
func findString(v any, keys ...string) string {
	wanted := map[string]bool{}
	for _, k := range keys {
		wanted[k] = true
	}
	var walk func(any) string
	walk = func(x any) string {
		if m, ok := x.(map[string]any); ok {
			for k, v := range m {
				if wanted[strings.ToLower(k)] {
					if s, ok := v.(string); ok && s != "" {
						return s
					}
				}
			}
			for _, v := range m {
				if s := walk(v); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}
func enrichDesktopMetadata(rec *Record, h http.Header) {
	rec.Client = firstNonEmpty(h.Get("Originator"), h.Get("User-Agent"))
	rec.ThreadID = firstNonEmpty(h.Get("Thread-Id"), h.Get("X-Thread-Id"))
	rec.WindowID = h.Get("X-Codex-Window-Id")
	raw := h.Get("X-Codex-Turn-Metadata")
	if raw == "" {
		return
	}
	var meta struct {
		SessionID    string `json:"session_id"`
		ThreadID     string `json:"thread_id"`
		TurnID       string `json:"turn_id"`
		WindowID     string `json:"window_id"`
		RequestKind  string `json:"request_kind"`
		ThreadSource string `json:"thread_source"`
		Workspaces   map[string]struct {
			AssociatedRemoteURLs map[string]string `json:"associated_remote_urls"`
		} `json:"workspaces"`
	}
	if json.Unmarshal([]byte(raw), &meta) != nil {
		return
	}
	rec.ThreadID = firstNonEmpty(meta.ThreadID, meta.SessionID, rec.ThreadID)
	rec.TurnID = meta.TurnID
	rec.WindowID = firstNonEmpty(meta.WindowID, rec.WindowID)
	rec.RequestKind = meta.RequestKind
	rec.ThreadSource = strings.ToLower(strings.TrimSpace(meta.ThreadSource))
	paths := make([]string, 0, len(meta.Workspaces))
	for p := range meta.Workspaces {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		rec.ProjectPath = paths[0]
		rec.ProjectName = filepath.Base(strings.ReplaceAll(paths[0], "\\", "/"))
		rem := meta.Workspaces[paths[0]].AssociatedRemoteURLs
		for _, k := range []string{"origin", "forgejo", "github", "llm"} {
			if rem[k] != "" {
				rec.GitRemote = rem[k]
				break
			}
		}
	}
}
func extractConversationSummary(body []byte) string {
	candidates := extractConversationSummaries(body)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

func extractConversationSummaryFirst(body []byte) string {
	candidates := extractConversationSummaries(body)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func extractConversationSummaries(body []byte) []string {
	for _, path := range []string{"title", "conversation.title", "metadata.title", "client_metadata.title"} {
		if value := compactSummary(gjson.GetBytes(body, path).String()); value != "" {
			return []string{value}
		}
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var candidates []string
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			role, _ := item["role"].(string)
			if strings.EqualFold(role, "user") {
				for _, key := range []string{"text", "prompt", "content", "input"} {
					collectSummaryStrings(item[key], &candidates)
				}
			}
			for _, key := range []string{"input", "messages"} {
				walk(item[key])
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(root)
	// Some OpenAI-compatible clients send a plain string instead of structured
	// role-tagged messages.
	if len(candidates) == 0 {
		if object, ok := root.(map[string]any); ok {
			collectSummaryStrings(object["input"], &candidates)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

func collectSummaryStrings(value any, candidates *[]string) {
	switch item := value.(type) {
	case string:
		if summary := meaningfulSummary(item); summary != "" {
			*candidates = append(*candidates, summary)
		}
	case []any:
		for _, child := range item {
			collectSummaryStrings(child, candidates)
		}
	case map[string]any:
		for _, key := range []string{"text", "input_text", "content"} {
			if child, ok := item[key]; ok {
				collectSummaryStrings(child, candidates)
			}
		}
	}
}

// extractConversationUserText returns the complete latest user-authored text
// from an original client request. Unlike extractConversationSummary, this is
// intentionally not length-limited. Callers should use it only on a detail
// route where the user explicitly asked to materialize one turn.
func extractConversationUserText(body []byte) string {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var candidates []string
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			role, _ := item["role"].(string)
			if strings.EqualFold(role, "user") {
				for _, key := range []string{"text", "prompt", "content", "input"} {
					collectUserTextStrings(item[key], &candidates)
				}
			}
			for _, key := range []string{"input", "messages"} {
				walk(item[key])
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(root)
	if len(candidates) == 0 {
		if object, ok := root.(map[string]any); ok {
			collectUserTextStrings(object["input"], &candidates)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

func collectUserTextStrings(value any, candidates *[]string) {
	switch item := value.(type) {
	case string:
		if text := meaningfulUserText(item); text != "" {
			*candidates = append(*candidates, text)
		}
	case []any:
		for _, child := range item {
			collectUserTextStrings(child, candidates)
		}
	case map[string]any:
		for _, key := range []string{"text", "input_text", "content"} {
			if child, ok := item[key]; ok {
				collectUserTextStrings(child, candidates)
			}
		}
	}
}

func meaningfulSummary(value string) string {
	return compactSummary(meaningfulUserText(value))
}

var t3ContextBlock = regexp.MustCompile(`(?is)<t3_context\b[^>]*>.*?</t3_context>`)
var assistantQuotePrefix = regexp.MustCompile(`(?i)^\[Assistant quote\]\((t3-citation://[^)]*)\)\s*`)
var slashCommandTag = regexp.MustCompile(`(?is)<command-(name|args|message)>(.*?)</command-(?:name|args|message)>`)

func meaningfulUserText(value string) string {
	value = strings.TrimSpace(html.UnescapeString(value))
	value = strings.TrimSpace(t3ContextBlock.ReplaceAllString(value, ""))
	comments := []string{}
	for {
		match := assistantQuotePrefix.FindStringSubmatch(value)
		if len(match) == 0 {
			break
		}
		if citation, err := url.Parse(match[1]); err == nil {
			if comment := citation.Query().Get("comment"); comment != "" {
				comments = append(comments, comment)
			}
		}
		value = strings.TrimSpace(strings.TrimPrefix(value, match[0]))
	}
	if value == "" {
		value = strings.Join(comments, "\n")
	}
	if strings.HasPrefix(value, "Delegated task node:delegated-task:command") {
		return ""
	}
	command, args := "", ""
	for _, tag := range slashCommandTag.FindAllStringSubmatch(value, -1) {
		switch strings.ToLower(tag[1]) {
		case "name":
			command = "/" + strings.TrimLeft(strings.TrimSpace(tag[2]), "/")
		case "args":
			args = strings.TrimSpace(tag[2])
		}
	}
	if command != "" {
		if args == "" {
			args = meaningfulUserText(slashCommandTag.ReplaceAllString(value, ""))
		}
		return strings.TrimSpace(command + " " + args)
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"\nuser prompt:\n", "\r\nuser prompt:\r\n", "\nuser message:\n", "\r\nuser message:\r\n"} {
		if pos := strings.LastIndex(lower, marker); pos >= 0 {
			value = strings.TrimSpace(value[pos+len(marker):])
			lower = strings.ToLower(value)
			break
		}
	}
	for {
		lower = strings.ToLower(value)
		matched := false
		for _, tag := range []string{"environment_context", "environment_info", "workspace_info", "in-app-browser-context", "app-context", "context", "editorcontext", "reminderinstructions", "codex_internal_context", "system-reminder", "command-name", "command-message", "command-args", "local-command-caveat", "local-command-stdout", "skill"} {
			open := "<" + tag
			if !strings.HasPrefix(lower, open) {
				continue
			}
			close := "</" + tag + ">"
			end := strings.Index(lower, close)
			if end < 0 {
				return ""
			}
			value = strings.TrimSpace(value[end+len(close):])
			matched = true
			break
		}
		if !matched {
			break
		}
	}
	value = unwrapSummaryTag(value, "userrequest")
	lower = strings.ToLower(value)
	if strings.HasPrefix(lower, "# files mentioned by the user:") {
		marker := "## my request for codex:"
		if pos := strings.Index(lower, marker); pos >= 0 {
			value = strings.TrimSpace(value[pos+len(marker):])
		} else {
			return ""
		}
	}
	lower = strings.ToLower(value)
	for _, prefix := range []string{"# agents.md instructions", "codebase and user instructions are shown below.", "this session is being continued from a previous conversation", "base directory for this skill:"} {
		if strings.HasPrefix(lower, prefix) {
			return ""
		}
	}
	return cleanTurnText(value)
}

func unwrapSummaryTag(value, tag string) string {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	if strings.HasPrefix(lower, open) && strings.HasSuffix(lower, close) {
		return strings.TrimSpace(trimmed[len(open) : len(trimmed)-len(close)])
	}
	return trimmed
}
func compactSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return value
}
func enrichGenericFacets(rec *Record, h http.Header, body []byte) {
	if rec.Facets == nil {
		rec.Facets = map[string][]string{}
	}
	if rec.ThreadSource == "" {
		rec.ThreadSource = extractThreadSource(body)
	}
	addFacet := func(k, v string) {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 2048 {
			return
		}
		for _, old := range rec.Facets[k] {
			if old == v {
				return
			}
		}
		rec.Facets[k] = append(rec.Facets[k], v)
	}
	var addResult func(string, gjson.Result)
	addResult = func(name string, result gjson.Result) {
		if result.IsArray() {
			for _, item := range result.Array() {
				addResult(name, item)
			}
			return
		}
		addFacet(name, result.String())
	}
	addPathValues := func(name, query string) {
		addResult(name, gjson.GetBytes(body, query))
	}
	for _, pair := range [][2]string{
		{"client", rec.Client}, {"client.user_agent", h.Get("User-Agent")},
		{"project.name", rec.ProjectName}, {"project.path", rec.ProjectPath}, {"git.remote", rec.GitRemote},
		{"session.id", rec.SessionID}, {"thread.id", rec.ThreadID}, {"turn.id", rec.TurnID}, {"window.id", rec.WindowID},
		{"request.kind", rec.RequestKind}, {"thread.source", rec.ThreadSource}, {"client.request_id", h.Get("X-Client-Request-Id")},
		{"request.id", h.Get("X-Request-Id")}, {"originator", h.Get("Originator")},
		{"sdk.language", h.Get("X-Stainless-Lang")}, {"sdk.package_version", h.Get("X-Stainless-Package-Version")},
		{"client.os", h.Get("X-Stainless-OS")}, {"client.arch", h.Get("X-Stainless-Arch")},
		{"runtime.name", h.Get("X-Stainless-Runtime")}, {"runtime.version", h.Get("X-Stainless-Runtime-Version")},
		{"anthropic.version", h.Get("Anthropic-Version")}, {"openai.organization", h.Get("OpenAI-Organization")},
		{"openai.project", h.Get("OpenAI-Project")},
	} {
		addFacet(pair[0], pair[1])
	}
	for _, name := range []string{"X-Claude-Code-Session-Id", "X-Session-Id", "Session-Id", "Thread-Id", "X-Thread-Id", "X-Conversation-Id", "X-Project-Id", "X-Workspace-Id"} {
		addFacet("header."+strings.ToLower(name), h.Get(name))
	}
	for _, spec := range [][2]string{
		{"session.id", "session_id"}, {"conversation.id", "conversation_id"}, {"thread.id", "thread_id"},
		{"project.name", "project.name"}, {"project.name", "project"}, {"project.id", "project_id"},
		{"workspace.name", "workspace.name"}, {"workspace.id", "workspace_id"},
		{"project.path", "project.path"}, {"project.path", "workspace.path"}, {"project.path", "cwd"},
		{"git.remote", "repository"}, {"git.remote", "repo"}, {"git.branch", "branch"},
		{"client.name", "metadata.client"}, {"client.name", "metadata.client_name"},
		{"client.version", "metadata.client_version"}, {"client.ide", "metadata.ide"},
		{"client.ide_version", "metadata.ide_version"}, {"client.os", "metadata.os"}, {"client.arch", "metadata.arch"},
		{"project.name", "metadata.project"}, {"workspace.name", "metadata.workspace"}, {"project.path", "metadata.cwd"},
		{"project.name", "client_metadata.project"}, {"workspace.name", "client_metadata.workspace"},
		{"reasoning.effort", "reasoning.effort"}, {"service.tier", "service_tier"}, {"tool.choice", "tool_choice"},
	} {
		addPathValues(spec[0], spec[1])
	}
	for _, spec := range [][2]string{
		{"tool.type", "tools.#.type"}, {"tool.name", "tools.#.name"}, {"tool.name", "tools.#.function.name"},
		{"input.type", "input.#.type"}, {"content.type", "input.#.content.#.type"},
		{"message.role", "messages.#.role"}, {"content.type", "messages.#.content.#.type"},
	} {
		addPathValues(spec[0], spec[1])
	}
	if rec.ProjectName == "" {
		rec.ProjectName = firstNonEmpty(firstFacet(rec.Facets, "project.name"), firstFacet(rec.Facets, "workspace.name"))
	}
	if rec.ProjectPath == "" {
		rec.ProjectPath = firstFacet(rec.Facets, "project.path")
	}
	if rec.GitRemote == "" {
		rec.GitRemote = firstFacet(rec.Facets, "git.remote")
	}
}

func extractThreadSource(body []byte) string {
	if direct := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_source").String()); direct != "" {
		return strings.ToLower(direct)
	}
	raw := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	if raw == "" {
		return ""
	}
	var meta struct {
		ThreadSource string `json:"thread_source"`
	}
	if json.Unmarshal([]byte(raw), &meta) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(meta.ThreadSource))
}
func firstFacet(facets map[string][]string, name string) string {
	if values := facets[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// archiveKeyID keeps attribution on the same identity used by CPA Manager Plus
// and the native access-policy plugin. Host caller_scope is intentionally a
// different salted fingerprint and remains only a compatibility fallback.
func archiveKeyID(metadata map[string]any) string {
	return firstNonEmpty(
		findString(metadata, "key_name", "key_alias", "principal_name", "key_id"),
		findString(metadata, "key_hash"),
		findString(metadata, "principal"),
		findString(metadata, "caller_scope"),
	)
}

func nativeKeyHash(headers http.Header) string {
	raw := strings.TrimSpace(headers.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		raw = strings.TrimSpace(raw[len("bearer "):])
	}
	if raw == "" {
		raw = strings.TrimSpace(headers.Get("X-API-Key"))
	}
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

func sanitizeMeta(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"key_id", "key_hash", "requested_model", "target_provider", "target_model", "group", "execution_session_id", "derived_session_id", "caller_scope", "request_path", "selected_auth_id"} {
		if v := findString(m, k); v != "" {
			out[k] = v
		}
	}
	return out
}
