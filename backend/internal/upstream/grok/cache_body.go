package grok

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// cache_body.go applies Grok cache identities and prepares native search
// companions from retained tool intent.
// Grok cache routing uses a Responses prompt_cache_key or a Chat conversation
// header after callers derive a scoped identity. Free-tier tool preparation
// needs the request's retained intent because earlier normalization can
// remove unsupported tools or choices. An absent identity removes the
// upstream cache key, and an empty Chat identity removes the routing header.
//
// Native search additions depend on the existing tool group and policy.
// Client functions retain their raw declarations unless a permitted search
// function becomes a native tool. Search names share a deduplication set
// across functions and native tools so a retained client search function
// reserves its name. Malformed or unsupported groups return the supplied
// body, and outgoing tool_choice remains the caller's responsibility during
// function-tool routing.

// Free cache preparation uses two native search declarations and a disabled
// tool choice, while Chat cache routing uses its dedicated conversation header.
const (
	grokFreeCacheNativeToolsJSON    = `[{"type":"web_search"},{"type":"x_search"}]` // native search tool declarations
	grokFreeCacheDisabledToolChoice = "none"                                        // disabled tool selection value
	grokConversationIDHeader        = "X-Grok-Conv-Id"                              // Chat session routing header
)

// claudeCodeSessionSuffixPattern extracts lowercase hex-and-hyphen session suffixes
// from metadata text; matching supplies a seed without validating UUID structure.
var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// ExtractClaudeCodeSessionIDFromPayload reads metadata.user_id as a trimmed
// string and extracts a recognized suffix or JSON-encoded session_id. Empty
// input or unrecognized metadata returns empty; the suffix pattern accepts
// lowercase hex and hyphens without validating UUID structure.
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
	// JSON-encoded metadata provides another session representation. Read its
	// session_id after suffix extraction fails so both forms participate in seed selection.
	if userID != "" && userID[0] == '{' {
		if sid := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sid != "" {
			return sid
		}
	}
	return ""
}

// ApplyGrokResponsesCacheIdentity writes a trimmed scoped identity into
// prompt_cache_key, or removes that field when identity is blank. Callers
// derive tenant isolation before invoking it. Supply intentSourceBody from
// before tool cleanup when injectFreeTierTools is enabled, so removed client
// intent remains visible to this routing decision.
//
// For a nonblank identity and Free-tier injection, retained tool intent
// prevents automatic tool addition. Tool-free intent installs web_search and
// x_search with tool_choice=none, making the added declarations disabled in
// the outgoing request. JSON mutation errors propagate, and function-tool
// routing is handled separately by ApplyGrokFreeToolCacheRoute.
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
	// Earlier request patching can remove unsupported tool fields. Inspect retained
	// intent here so that cleanup cannot turn a declared tool request into tool-free routing.
	if HasGrokResponsesToolIntent(intentSourceBody) {
		return out, nil
	}
	out, err = sjson.SetRawBytes(out, "tools", []byte(grokFreeCacheNativeToolsJSON))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", grokFreeCacheDisabledToolChoice)
}

// HasGrokResponsesToolIntent recognizes tools or tool_choice field presence,
// including empty values. An input additional_tools item also counts unless
// its own tools field is an empty array. Other shapes without these signals
// return false for automatic cache-tool injection.
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

// ApplyGrokFreeToolCacheRoute prepares tool routing for known Free requests
// with a nonblank cache identity and supported retained intent. Other
// requests return the supplied body. Callers retain intentSourceBody before
// cleanup and supply policy flags for client companions and search-function
// conversion.
//
// A retained tool_choice=none uses the client-companion path while disabling
// search-function conversion. This helper leaves the outgoing choice
// unchanged, so callers retain none in body when injected native tools must
// remain disabled. Other accepted choices pass the supplied policy flags
// through to AppendGrokFreeCacheNativeToolsWithPolicy.
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
		// Retained none allows cache companions without converting client search functions.
		// The body must still carry none because this helper does not rewrite tool_choice.
		return AppendGrokFreeCacheNativeToolsWithPolicy(body, true, false)
	}
	return AppendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, allowFunctionSearch)
}

// IsGrokFreeCacheFunctionToolIntent accepts a nonempty array of supported
// Responses tools with absent, auto, or none string tool choice. Function
// tools need a top-level nonblank name and no nested function field; forced
// objects, unsupported types, or malformed declarations return false.
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
			// This route consumes Responses declarations with top-level function names.
			// Reject a nested Chat function shape before policy can replace or append search tools.
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

// AppendGrokFreeCacheNativeTools delegates native-search preparation with
// search-function conversion enabled. allowPureClientTools determines whether
// client-only groups can receive search companions; unsupported, empty, or
// otherwise ineligible bodies are returned unchanged by the policy helper.
func AppendGrokFreeCacheNativeTools(body []byte, allowPureClientTools bool) ([]byte, error) {
	return AppendGrokFreeCacheNativeToolsWithPolicy(body, allowPureClientTools, true)
}

// AppendGrokFreeCacheNativeToolsWithPolicy rebuilds an eligible tool array
// with missing native search companions. Missing, empty, malformed,
// unsupported, or ineligible groups return the supplied body. Within a
// published rebuild, raw client/tool objects are retained while
// allowFunctionSearch converts web_search or x_search functions to native
// type-only declarations.
//
// One name set covers native search and retained same-named functions.
// Duplicate native names are suppressed, and retained search functions
// reserve their names against additions. A companion tool is required before
// publishing the rebuild; native-search-only groups keep their original
// array. Unless allowPureClientTools is true, an existing search name must
// also be present. The helper changes tools alone, leaving tool-choice policy
// to its caller.
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
	// Validate the complete group before publishing its rebuilt array. An unsupported
	// or malformed later item returns the supplied body, discarding earlier conversions.
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
			// Conversion places search functions into the native name set before additions,
			// so repeated declarations cannot introduce multiple native tools with the same name.
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
				// A retained search function occupies its name even when conversion is disabled,
				// preventing a native companion from competing with that caller-provided function.
				present[name] = true
			}
			hasCompanionTool = true
			merged = append(merged, json.RawMessage(tool.Raw))
		case "web_search", "x_search":
			// Native names share the same set as converted and reserved functions, so a
			// duplicate search declaration is omitted from any rebuild that gets published.
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
	// A client-only group needs explicit permission before search is added. When
	// permission is absent, require an existing search name so companions cannot change
	// automatic selection for a request that declared only local client tools.
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

// ApplyGrokCacheHeaders writes the trimmed identity to X-Grok-Conv-Id. A
// blank identity removes that header, and nil maps are ignored. Callers
// derive a scoped identity and serialize writes to a shared Header map.
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

// StripGrokChatPromptCacheKey removes a Responses prompt_cache_key from a
// Chat body when present. Derive the session identity before calling it, then
// apply that identity through the Chat routing header; absent fields retain
// the supplied bytes.
func StripGrokChatPromptCacheKey(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_key")
}
