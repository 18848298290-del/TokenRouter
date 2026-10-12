package grok

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokFreeCacheNativeToolsJSON    = `[{"type":"web_search"},{"type":"x_search"}]`
	grokFreeCacheDisabledToolChoice = "none"
	grokConversationIDHeader        = "X-Grok-Conv-Id"
)

// claudeCodeSessionSuffixPattern matches the _session_<uuid> suffix in Claude Code metadata.user_id.
var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

func ExtractClaudeCodeSessionIDFromPayload(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	if userID == "" {
		return ""
	}
	if matches := claudeCodeSessionSuffixPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return matches[1]
	}
	// Some Claude Code clients encode user_id as JSON with a session_id field.
	if userID != "" && userID[0] == '{' {
		if sid := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sid != "" {
			return sid
		}
	}
	return ""
}

// ApplyGrokResponsesCacheIdentity replaces the xAI Responses cache key with the supplied identity.
// Callers supply a tenant-scoped identity to isolate caches on shared OAuth providers.
//
// Free OAuth requests need native search tools to select a cache-capable route.
// For tool-free requests, inject native tools with tool_choice=none so search stays disabled.
// Function-tool requests use ApplyGrokFreeToolCacheRoute for Messages, native Responses,
// and the WebSocket HTTP bridge.
func ApplyGrokResponsesCacheIdentity(body, intentSourceBody []byte, identity string, injectFreeTierTools bool) ([]byte, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return sjson.DeleteBytes(body, "prompt_cache_key")
		}
		return body, nil
	}
	out, err := sjson.SetBytes(body, "prompt_cache_key", identity)
	if err != nil {
		return nil, err
	}
	if !injectFreeTierTools {
		return out, nil
	}
	// Inspect the request before cleanup: patchGrokResponsesBody can remove unsupported tools
	// and tool_choice, which would otherwise make explicit tool intent appear tool-free.
	if HasGrokResponsesToolIntent(intentSourceBody) {
		return out, nil
	}
	out, err = sjson.SetRawBytes(out, "tools", []byte(grokFreeCacheNativeToolsJSON))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", grokFreeCacheDisabledToolChoice)
}

func HasGrokResponsesToolIntent(body []byte) bool {
	if gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "tool_choice").Exists() {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		tools := item.Get("tools")
		if !tools.Exists() || !tools.IsArray() || len(tools.Array()) > 0 {
			return true
		}
	}
	return false
}

func ApplyGrokFreeToolCacheRoute(body, intentSourceBody []byte, knownFree bool, cacheIdentity string, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	if strings.TrimSpace(cacheIdentity) == "" || !knownFree {
		return body, nil
	}
	intentTools := gjson.GetBytes(intentSourceBody, "tools")
	intentToolChoice := gjson.GetBytes(intentSourceBody, "tool_choice")
	if !IsGrokFreeCacheFunctionToolIntent(intentTools, intentToolChoice) {
		return body, nil
	}
	if intentToolChoice.Type == gjson.String && strings.TrimSpace(intentToolChoice.String()) == grokFreeCacheDisabledToolChoice {
		// tool_choice=none keeps injected native tools disabled.
		return AppendGrokFreeCacheNativeToolsWithPolicy(body, true, false)
	}
	return AppendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, allowFunctionSearch)
}

func IsGrokFreeCacheFunctionToolIntent(tools, toolChoice gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	items := tools.Array()
	if len(items) == 0 {
		return false
	}
	for _, tool := range items {
		if !tool.IsObject() {
			return false
		}
		toolType := strings.TrimSpace(tool.Get("type").String())
		if _, ok := grokResponsesSupportedToolTypes[toolType]; !ok {
			return false
		}
		if toolType == "function" {
			// Responses function names are top-level; nested Chat Completions declarations are rejected.
			if strings.TrimSpace(tool.Get("name").String()) == "" || tool.Get("function").Exists() {
				return false
			}
		}
	}
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type != gjson.String {
		return false
	}
	switch strings.TrimSpace(toolChoice.String()) {
	case "auto", grokFreeCacheDisabledToolChoice:
		return true
	default:
		return false
	}
}

func AppendGrokFreeCacheNativeTools(body []byte, allowPureClientTools bool) ([]byte, error) {
	return AppendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, true)
}

func AppendGrokFreeCacheNativeToolsWithPolicy(body []byte, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, nil
	}

	items := tools.Array()
	if len(items) == 0 {
		return body, nil
	}
	hasNativeSearch := false
	for _, tool := range items {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "web_search", "x_search":
			hasNativeSearch = true
		}
	}
	if !allowPureClientTools && !allowFunctionSearch && !hasNativeSearch {
		return body, nil
	}
	merged := make([]json.RawMessage, 0, len(items)+2)
	present := make(map[string]bool, 2)
	hasCompanionTool := false
	for _, tool := range items {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "function":
			name := strings.TrimSpace(tool.Get("name").String())
			if !tool.IsObject() || name == "" || tool.Get("function").Exists() {
				return body, nil
			}
			// Convert Grok Build search functions to native tools when permitted, enabling the
			// cache-capable Free OAuth route and deduplicating search tool names.
			if (name == "web_search" || name == "x_search") && allowFunctionSearch {
				if present[name] {
					continue
				}
				raw, err := json.Marshal(map[string]string{"type": name})
				if err != nil {
					return nil, err
				}
				merged = append(merged, raw)
				present[name] = true
				if allowPureClientTools {
					hasCompanionTool = true
				}
				continue
			}
			if name == "web_search" || name == "x_search" {
				// Reserve the client function name so a same-named native tool is not added.
				present[name] = true
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		case "web_search", "x_search":
			// Deduplicate native search tools so repeated calls do not accumulate copies.
			if present[toolType] {
				continue
			}
			merged = append(merged, json.RawMessage(tool.Raw))
			present[toolType] = true
		default:
			if _, ok := grokResponsesSupportedToolTypes[toolType]; !ok {
				return body, nil
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		}
	}
	if !hasCompanionTool {
		return body, nil
	}
	// Unless pure client tools are allowed, require existing search intent before adding search tools.
	// This prevents tools such as view_image from changing automatic tool selection (#4486).
	if !allowPureClientTools && !present["web_search"] && !present["x_search"] {
		return body, nil
	}
	for _, toolType := range []string{"web_search", "x_search"} {
		if present[toolType] {
			continue
		}
		raw, err := json.Marshal(map[string]string{"type": toolType})
		if err != nil {
			return nil, err
		}
		merged = append(merged, raw)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}

// ApplyGrokCacheHeaders replaces the Chat Completions session routing header with identity.
// An empty identity removes the header.
func ApplyGrokCacheHeaders(headers http.Header, identity string) {
	if headers == nil {
		return
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		headers.Del(grokConversationIDHeader)
		return
	}
	headers.Set(grokConversationIDHeader, identity)
}

// StripGrokChatPromptCacheKey removes the Responses field after callers derive the identity.
// Chat Completions routes its cache through the session header.
func StripGrokChatPromptCacheKey(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_key")
}
