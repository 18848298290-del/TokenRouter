package bedrock

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	claude "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

const (
	DefaultBedrockRegion = "us-east-1"

	// FeatureKeyBedrockCCCompat names the Bedrock Claude Code compatibility group setting.
	FeatureKeyBedrockCCCompat = "bedrock_cc_compat"

	BedrockContextManagementBetaToken = "context-management-2025-06-27"
	DefaultThinkingBudgetTokens       = 10000
	DefaultCCMaxTokens                = 81920
)

var (
	BedrockCrossRegionPrefixes = []string{"us.", "eu.", "apac.", "jp.", "au.", "us-gov.", "global."}

	// BedrockSupportedBetaTokens lists beta tokens allowed in Invoke requests.
	// Sources: AWS Bedrock documentation and litellm anthropic_beta_headers_config.json.
	// Verify upstream support when updating this allowlist.
	BedrockSupportedBetaTokens = map[string]bool{
		"computer-use-2025-01-24":                true,
		"computer-use-2025-11-24":                true,
		"context-1m-2025-08-07":                  true,
		"context-management-2025-06-27":          true, // context compaction and clear_thinking
		"compact-2026-01-12":                     true, // InvokeModel compaction for supported Opus models
		"fine-grained-tool-streaming-2025-05-14": true, // fine-grained tool streaming
		// interleaved-thinking-2025-05-14 is excluded pending documented Bedrock support.
		"tool-search-tool-2025-10-19": true,
		"tool-examples-2025-10-29":    true,
	}

	// BedrockBetaTokenTransforms maps Anthropic beta tokens to Bedrock equivalents.
	BedrockBetaTokenTransforms = map[string]string{
		"advanced-tool-use-2025-11-20": "tool-search-tool-2025-10-19",
	}

	// BedrockToolUseIDRe matches characters outside the allowed tool-use ID alphabet.
	BedrockToolUseIDRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

func BedrockRuntimeRegion(provider *RouteInput) string {
	if provider == nil {
		return DefaultBedrockRegion
	}
	if region := strings.TrimSpace(provider.Region); region != "" {
		return region
	}
	return DefaultBedrockRegion
}

func ShouldForceBedrockGlobal(provider *RouteInput) bool {
	return provider != nil && provider.ForceGlobal
}

func IsRegionalBedrockModelID(modelID string) bool {
	for _, prefix := range BedrockCrossRegionPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
}

func IsLikelyBedrockModelID(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "arn:") {
		return true
	}
	for _, prefix := range []string{
		"anthropic.",
		"amazon.",
		"meta.",
		"mistral.",
		"cohere.",
		"ai21.",
		"deepseek.",
		"stability.",
		"writer.",
		"nova.",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return IsRegionalBedrockModelID(lower)
}

// ResolveBedrockModelID returns the routed model ID and whether resolution succeeded.
func ResolveBedrockModelID(provider *RouteInput, requestedModel string) (string, bool) {
	route, err := ResolveBedrockModelRoute(provider, requestedModel)
	return route.ModelID, err == nil
}

// BuildBedrockURL builds the regional InvokeModel endpoint.
// Streaming requests use invoke-with-response-stream.
// The model ID is path-escaped, with colons encoded separately.
func BuildBedrockURL(region, modelID string, stream bool) string {
	if region == "" {
		region = DefaultBedrockRegion
	}
	encodedModelID := url.PathEscape(modelID)
	// PathEscape leaves colons unchanged; encode them for the Bedrock model path.
	encodedModelID = strings.ReplaceAll(encodedModelID, ":", "%3A")
	if stream {
		return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke-with-response-stream", region, encodedModelID)
	}
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke", region, encodedModelID)
}

// PrepareBedrockRequestBody resolves beta tokens and prepares an Invoke request body.
// It sets the protocol version, removes unsupported fields, inlines output schemas,
// and cleans tool definitions and cache controls.
// Thinking and tool-use ID normalization require the ccCompat option in
// PrepareBedrockRequestBodyWithTokens.
func PrepareBedrockRequestBody(body []byte, modelID string, betaHeader string) ([]byte, error) {
	betaTokens := ResolveBedrockBetaTokens(betaHeader, body, modelID)
	return PrepareBedrockRequestBodyWithTokens(body, modelID, betaTokens, false)
}

// PrepareBedrockRequestBodyWithTokens prepares an Invoke body with precomputed beta tokens.
// ccCompat also normalizes thinking settings and tool-use IDs.
func PrepareBedrockRequestBodyWithTokens(body []byte, modelID string, betaTokens []string, ccCompat bool, rules ...modelcatalog.ExecutionRules) ([]byte, error) {
	var err error

	betaTokens = FilterBedrockBetaTokens(betaTokens)
	body = SanitizeBedrockFieldsForBetaTokens(body, betaTokens)

	// Bedrock carries the protocol version in the body.
	body, err = sjson.SetBytes(body, "anthropic_version", "bedrock-2023-05-31")
	if err != nil {
		return nil, fmt.Errorf("inject anthropic_version: %w", err)
	}

	// Bedrock receives the filtered beta tokens in the request body.
	if len(betaTokens) > 0 {
		body, err = sjson.SetBytes(body, "anthropic_beta", betaTokens)
		if err != nil {
			return nil, fmt.Errorf("inject anthropic_beta: %w", err)
		}
		logger.LegacyPrintf("service.gateway", "[Bedrock] Injected beta tokens: %v (model=%s ccCompat=%v)", betaTokens, modelID, ccCompat)
	} else {
		body, _ = sjson.DeleteBytes(body, "anthropic_beta")
	}

	// Drop direct-API metadata that is not part of this Invoke payload.
	body, _ = sjson.DeleteBytes(body, "provider")
	body, _ = sjson.DeleteBytes(body, "metadata")

	// The model is selected by the URL.
	body, err = sjson.DeleteBytes(body, "model")
	if err != nil {
		return nil, fmt.Errorf("remove model field: %w", err)
	}

	// The endpoint selects streaming; the body has no stream flag.
	body, err = sjson.DeleteBytes(body, "stream")
	if err != nil {
		return nil, fmt.Errorf("remove stream field: %w", err)
	}

	// Supply the requested schema as user text because Invoke omits output_format.
	// See litellm _convert_output_format_to_inline_schema for the corresponding adaptation.
	body = ConvertOutputFormatToInlineSchema(body)

	body, err = sjson.DeleteBytes(body, "output_config")
	if err != nil {
		return nil, fmt.Errorf("remove output_config field: %w", err)
	}

	// Remove the Claude Code custom tool extension before sending to Bedrock.
	// For example, custom: {defer_loading: true} is outside the Invoke tool schema.
	body = RemoveCustomFieldFromTools(body)

	body = SanitizeBedrockCacheControl(body, modelID, rules...)

	if ccCompat {
		body = SanitizeBedrockThinking(body, modelID, rules...)
		body = SanitizeBedrockToolUseIDs(body)
	}

	return body, nil
}

// ResolveBedrockBetaTokens merges header and body-derived tokens, then filters them for Bedrock.
func ResolveBedrockBetaTokens(betaHeader string, body []byte, modelID string, rules ...modelcatalog.ExecutionRules) []string {
	betaTokens := ParseAnthropicBetaHeader(betaHeader)
	betaTokens = AutoInjectBedrockBetaTokens(betaTokens, body, modelID, rules...)
	return FilterBedrockBetaTokens(betaTokens)
}

// ConvertOutputFormatToInlineSchema removes object-valued output_format and appends its schema
// as text to the last user message when that message has string or array content.
// Missing schemas or suitable user messages leave the field removed without adding text.
func ConvertOutputFormatToInlineSchema(body []byte) []byte {
	outputFormat := gjson.GetBytes(body, "output_format")
	if !outputFormat.Exists() || !outputFormat.IsObject() {
		return body
	}

	body, _ = sjson.DeleteBytes(body, "output_format")

	schema := outputFormat.Get("schema")
	if !schema.Exists() {
		return body
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	msgArr := messages.Array()
	lastUserIdx := -1
	for i, m := range slices.Backward(msgArr) {
		if m.Get("role").String() == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return body
	}

	schemaJSON, err := json.Marshal(json.RawMessage(schema.Raw))
	if err != nil {
		return body
	}

	content := msgArr[lastUserIdx].Get("content")
	basePath := fmt.Sprintf("messages.%d.content", lastUserIdx)

	if content.IsArray() {
		idx := len(content.Array())
		body, _ = sjson.SetBytes(body, fmt.Sprintf("%s.%d.type", basePath, idx), "text")
		body, _ = sjson.SetBytes(body, fmt.Sprintf("%s.%d.text", basePath, idx), string(schemaJSON))
	} else if content.Type == gjson.String {
		// Preserve string content as the first text block when adding the schema.
		originalText := content.String()
		body, _ = sjson.SetBytes(body, basePath, []map[string]string{
			{"type": "text", "text": originalText},
			{"type": "text", "text": string(schemaJSON)},
		})
	}

	return body
}

// RemoveCustomFieldFromTools removes the custom extension from each tool definition.
func RemoveCustomFieldFromTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body
	}
	var err error
	for i := range tools.Array() {
		body, err = sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.custom", i))
		if err != nil {
			// Continue processing other tools when a deletion fails.
			continue
		}
	}
	return body
}

// IsBedrockClaude45OrNewer reports whether the catalog declares cache TTL support.
func IsBedrockClaude45OrNewer(modelID string) bool {
	return len(modelcatalog.BedrockExecutionRules(modelID).CacheTTL) > 0
}

// SanitizeBedrockCacheControl removes unsupported cache fields from system and message blocks.
// It removes scope and retains TTL only when the model rules declare TTL support.
// Supported TTL values are "5m" and "1h".
func SanitizeBedrockCacheControl(body []byte, modelID string, rules ...modelcatalog.ExecutionRules) []byte {
	isClaude45 := len(bedrockExecutionRules(modelID, rules).CacheTTL) > 0

	systemArr := gjson.GetBytes(body, "system")
	if systemArr.Exists() && systemArr.IsArray() {
		for i, item := range systemArr.Array() {
			if !item.IsObject() {
				continue
			}
			cc := item.Get("cache_control")
			if !cc.Exists() || !cc.IsObject() {
				continue
			}
			body = DeleteCacheControlUnsupportedFields(body, fmt.Sprintf("system.%d.cache_control", i), cc, isClaude45)
		}
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	for mi, msg := range messages.Array() {
		if !msg.IsObject() {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		for ci, block := range content.Array() {
			if !block.IsObject() {
				continue
			}
			cc := block.Get("cache_control")
			if !cc.Exists() || !cc.IsObject() {
				continue
			}
			body = DeleteCacheControlUnsupportedFields(body, fmt.Sprintf("messages.%d.content.%d.cache_control", mi, ci), cc, isClaude45)
		}
	}

	return body
}

// DeleteCacheControlUnsupportedFields removes scope and unsupported TTL values at basePath.
func DeleteCacheControlUnsupportedFields(body []byte, basePath string, cc gjson.Result, isClaude45 bool) []byte {
	if cc.Get("scope").Exists() {
		body, _ = sjson.DeleteBytes(body, basePath+".scope")
	}

	// The capability flag permits only the recognized TTL values.
	ttl := cc.Get("ttl")
	if ttl.Exists() {
		shouldRemove := true
		if isClaude45 {
			v := ttl.String()
			if v == "5m" || v == "1h" {
				shouldRemove = false
			}
		}
		if shouldRemove {
			body, _ = sjson.DeleteBytes(body, basePath+".ttl")
		}
	}

	return body
}

func ParseAnthropicBetaHeader(header string) []string { return claude.ParseAnthropicBetaHeader(header) }

// AutoInjectBedrockBetaTokens adds beta tokens required by tool features in the body.
// The corresponding litellm helpers are get_anthropic_beta_list and
// _get_tool_search_beta_header_for_bedrock.
//
// Some clients enable a body feature without sending its beta header.
// The caller filters and translates the resulting tokens before sending.
func AutoInjectBedrockBetaTokens(tokens []string, body []byte, modelID string, rules ...modelcatalog.ExecutionRules) []string {
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		seen[t] = true
	}

	inject := func(token string) {
		if !seen[token] {
			tokens = append(tokens, token)
			seen[token] = true
		}
	}

	// Thinking alone does not add the unlisted interleaved-thinking beta.

	// Computer tools select the computer-use beta.
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		toolSearchUsed := false
		programmaticToolCallingUsed := false
		inputExamplesUsed := false
		for _, tool := range tools.Array() {
			toolType := tool.Get("type").String()
			if strings.HasPrefix(toolType, "computer_20") {
				inject("computer-use-2025-11-24")
			}
			if IsBedrockToolSearchType(toolType) {
				toolSearchUsed = true
			}
			if HasCodeExecutionAllowedCallers(tool) {
				programmaticToolCallingUsed = true
			}
			if HasInputExamples(tool) {
				inputExamplesUsed = true
			}
		}
		if programmaticToolCallingUsed || inputExamplesUsed {
			// Programmatic callers and examples select advanced-tool-use here.
			// FilterBedrockBetaTokens later translates it to the Bedrock tool-search token.
			inject("advanced-tool-use-2025-11-20")
		}
		rule := bedrockExecutionRules(modelID, rules)
		if toolSearchUsed && rule.ToolSearch != nil && *rule.ToolSearch {
			// Tool search on an eligible model can use the Bedrock token directly.
			if !programmaticToolCallingUsed && !inputExamplesUsed {
				inject("tool-search-tool-2025-10-19")
			} else {
				inject("advanced-tool-use-2025-11-20")
			}
		}
	}

	return tokens
}

func IsBedrockToolSearchType(toolType string) bool {
	return toolType == "tool_search_tool_regex_20251119" || toolType == "tool_search_tool_bm25_20251119"
}

func HasCodeExecutionAllowedCallers(tool gjson.Result) bool {
	allowedCallers := tool.Get("allowed_callers")
	if ContainsStringInJSONArray(allowedCallers, "code_execution_20250825") {
		return true
	}
	return ContainsStringInJSONArray(tool.Get("function.allowed_callers"), "code_execution_20250825")
}

func HasInputExamples(tool gjson.Result) bool {
	if arr := tool.Get("input_examples"); arr.Exists() && arr.IsArray() && len(arr.Array()) > 0 {
		return true
	}
	arr := tool.Get("function.input_examples")
	return arr.Exists() && arr.IsArray() && len(arr.Array()) > 0
}

func ContainsStringInJSONArray(result gjson.Result, target string) bool {
	if !result.Exists() || !result.IsArray() {
		return false
	}
	for _, item := range result.Array() {
		if item.String() == target {
			return true
		}
	}
	return false
}

// BedrockModelSupportsToolSearch reports the catalog's tool-search capability.
func BedrockModelSupportsToolSearch(modelID string) bool {
	rules := modelcatalog.BedrockExecutionRules(modelID)
	return rules.ToolSearch != nil && *rules.ToolSearch
}

// FilterBedrockBetaTokens translates and deduplicates tokens in the Invoke allowlist.
// It also adds tool-examples when tool-search-tool is selected.
func FilterBedrockBetaTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	var result []string

	for _, t := range tokens {
		if replacement, ok := BedrockBetaTokenTransforms[t]; ok {
			t = replacement
		}
		if BedrockSupportedBetaTokens[t] && !seen[t] {
			result = append(result, t)
			seen[t] = true
		}
	}

	// The tool-search token also enables tool examples.
	if seen["tool-search-tool-2025-10-19"] && !seen["tool-examples-2025-10-29"] {
		result = append(result, "tool-examples-2025-10-29")
	}

	return result
}

// SanitizeBedrockFieldsForBetaTokens removes fields whose required beta tokens are absent.
func SanitizeBedrockFieldsForBetaTokens(body []byte, betaTokens []string) []byte {
	if !ContainsBedrockBetaToken(betaTokens, BedrockContextManagementBetaToken) && gjson.GetBytes(body, "context_management").Exists() {
		body, _ = sjson.DeleteBytes(body, "context_management")
	}
	if !ContainsBedrockBetaToken(betaTokens, claude.BetaServerSideFallback) && gjson.GetBytes(body, "fallbacks").Exists() {
		body, _ = sjson.DeleteBytes(body, "fallbacks")
	}
	if !ContainsAnyBedrockBetaToken(betaTokens, claude.BetaServerSideFallback, claude.BetaFallbackCredit, claude.BetaFallbackCreditLegacy) &&
		gjson.GetBytes(body, "fallback_credit_token").Exists() {
		body, _ = sjson.DeleteBytes(body, "fallback_credit_token")
	}
	return body
}

// ContainsBedrockBetaToken reports whether the token list contains target.
func ContainsBedrockBetaToken(tokens []string, target string) bool {
	return slices.Contains(tokens, target)
}

// ContainsAnyBedrockBetaToken reports whether any target token is present.
func ContainsAnyBedrockBetaToken(tokens []string, targets ...string) bool {
	for _, target := range targets {
		if ContainsBedrockBetaToken(tokens, target) {
			return true
		}
	}
	return false
}

// IsBedrockOpus47OrNewer checks for an Opus model with catalog-declared adaptive thinking.
func IsBedrockOpus47OrNewer(modelID string) bool {
	return modelcatalog.BedrockExecutionRules(modelID).ThinkingMode == "adaptive" && strings.Contains(strings.ToLower(modelID), "opus")
}

func IsBedrockFable5(modelID string) bool {
	return modelcatalog.BedrockExecutionRules(modelID).ThinkingMode == "adaptive" && strings.Contains(strings.ToLower(modelID), "fable")
}

// SanitizeBedrockThinking normalizes thinking according to the resolved model mode.
// Converting enabled to adaptive removes the budget; adaptive Fable requests also drop it.
// Budget-based enabled thinking receives a default when budget_tokens is missing.
func SanitizeBedrockThinking(body []byte, modelID string, rules ...modelcatalog.ExecutionRules) []byte {
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() || !thinking.IsObject() {
		return body
	}

	thinkingType := thinking.Get("type").String()
	if thinkingType == "" {
		return body
	}

	mode := bedrockExecutionRules(modelID, rules).ThinkingMode
	if mode == "adaptive" || mode == "adaptive_between_tools" {
		if thinkingType == "enabled" {
			body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
		}
		if thinkingType == "enabled" || thinkingType == "adaptive" && IsBedrockFable5(modelID) {
			body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
		return body
	}

	if thinkingType == "enabled" && !thinking.Get("budget_tokens").Exists() {
		body, _ = sjson.SetBytes(body, "thinking.budget_tokens", DefaultThinkingBudgetTokens)
	}

	return body
}

// SanitizeBedrockToolUseIDs replaces unsupported characters in tool-use and tool-result IDs.
// The accepted alphabet is ASCII letters, digits, underscores, and hyphens.
func SanitizeBedrockToolUseIDs(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	for mi, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		for ci, block := range content.Array() {
			switch block.Get("type").String() {
			case "tool_use":
				body = SanitizeIDField(body, block.Get("id").String(), fmt.Sprintf("messages.%d.content.%d.id", mi, ci))
			case "tool_result":
				body = SanitizeIDField(body, block.Get("tool_use_id").String(), fmt.Sprintf("messages.%d.content.%d.tool_use_id", mi, ci))
			}
		}
	}
	return body
}

func SanitizeIDField(body []byte, id, path string) []byte {
	if id == "" {
		return body
	}
	sanitized := BedrockToolUseIDRe.ReplaceAllString(id, "_")
	if sanitized != id {
		body, _ = sjson.SetBytes(body, path, sanitized)
	}
	return body
}

// SanitizeBedrockCCFields removes unsupported Claude Code fields and fills required defaults.
// It removes service_tier, interface_geo, context_management, fallbacks, and
// fallback_credit_token regardless of the beta list.
// Missing max_tokens and anthropic_version receive Bedrock defaults.
func SanitizeBedrockCCFields(body []byte) []byte {
	if gjson.GetBytes(body, "service_tier").Exists() {
		body, _ = sjson.DeleteBytes(body, "service_tier")
	}
	if gjson.GetBytes(body, "interface_geo").Exists() {
		body, _ = sjson.DeleteBytes(body, "interface_geo")
	}
	if gjson.GetBytes(body, "context_management").Exists() {
		body, _ = sjson.DeleteBytes(body, "context_management")
	}
	if gjson.GetBytes(body, "fallbacks").Exists() {
		body, _ = sjson.DeleteBytes(body, "fallbacks")
	}
	if gjson.GetBytes(body, "fallback_credit_token").Exists() {
		body, _ = sjson.DeleteBytes(body, "fallback_credit_token")
	}
	if !gjson.GetBytes(body, "max_tokens").Exists() {
		body, _ = sjson.SetBytes(body, "max_tokens", DefaultCCMaxTokens)
	}
	if !gjson.GetBytes(body, "anthropic_version").Exists() {
		body, _ = sjson.SetBytes(body, "anthropic_version", "bedrock-2023-05-31")
	}
	return body
}

// SanitizeBedrockCCBetaTokens normalizes an existing anthropic_beta field for Invoke.
// Unsupported tokens are removed to avoid upstream validation errors.
func SanitizeBedrockCCBetaTokens(body []byte, modelID string, rules ...modelcatalog.ExecutionRules) []byte {
	betaField := gjson.GetBytes(body, "anthropic_beta")
	if !betaField.Exists() {
		return body
	}

	var tokens []string
	if betaField.IsArray() {
		for _, t := range betaField.Array() {
			if t.Type == gjson.String {
				tokens = append(tokens, t.String())
			}
		}
	}

	originalTokens := append([]string(nil), tokens...) // retained for diagnostics
	// Body features can require beta tokens even when the existing token list is empty.

	tokens = AutoInjectBedrockBetaTokens(tokens, body, modelID, rules...)
	tokens = FilterBedrockBetaTokens(tokens)

	if len(tokens) == 0 {
		body, _ = sjson.DeleteBytes(body, "anthropic_beta")
		logger.LegacyPrintf("service.gateway", "[Bedrock CC Compat] Removed all beta tokens: original=%v", originalTokens)
	} else {
		body, _ = sjson.SetBytes(body, "anthropic_beta", tokens)
		if len(originalTokens) > 0 {
			logger.LegacyPrintf("service.gateway", "[Bedrock CC Compat] Filtered beta tokens: original=%v final=%v", originalTokens, tokens)
		} else {
			logger.LegacyPrintf("service.gateway", "[Bedrock CC Compat] Auto-injected beta tokens: %v", tokens)
		}
	}

	return body
}

func bedrockExecutionRules(model string, rules []modelcatalog.ExecutionRules) modelcatalog.ExecutionRules {
	if len(rules) > 0 {
		return rules[0]
	}
	return modelcatalog.BedrockExecutionRules(model)
}
