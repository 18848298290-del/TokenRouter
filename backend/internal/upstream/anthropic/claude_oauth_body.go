package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ClaudeFableOAuthSystemPromptBlocks keeps the Claude Code identity required by
// OAuth credentials without the generic CLI expansion block. Fable 5 rejects
// that expansion upstream with stop_reason=refusal and zero output tokens,
// while the native billing + identity shape is accepted. Original client
// system instructions are still migrated into the message history by
// RewriteSystemForNonClaudeCodeWithPromptBlocks.
const ClaudeFableOAuthSystemPromptBlocks = `[
	{"type":"text","text":"{billing_header}"},
	{"type":"text","text":"{claude_code_system_prompt}"}
]`

type AnthropicCacheControlPayload struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}
type AnthropicSystemTextBlockPayload struct {
	Type         string                        `json:"type"`
	Text         string                        `json:"text"`
	CacheControl *AnthropicCacheControlPayload `json:"cache_control,omitempty"`
}
type AnthropicMetadataPayload struct {
	UserID string `json:"user_id"`
}
type ClaudeOAuthNormalizeOptions struct {
	InjectMetadata          bool
	MetadataUserID          string
	StripSystemCacheControl bool
}

type ClaudeOAuthSystemPromptBlockConfig struct {
	Enabled      *bool           `json:"enabled,omitempty"`
	Type         string          `json:"type,omitempty"`
	Text         string          `json:"text,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}
type ClaudeOAuthSystemPromptBlocksEnvelope struct {
	Blocks []ClaudeOAuthSystemPromptBlockConfig `json:"blocks"`
}

type CacheControlPath struct {
	Path string
	Log  string
}

// SanitizeSystemText replaces the fixed OpenCode identity sentence.
// Other user instructions remain unchanged.
func SanitizeSystemText(text string) string {
	if text == "" {
		return text
	}
	// Replace the recognizable client banner with the Claude Code identity.
	text = strings.ReplaceAll(
		text,
		"You are OpenCode, the best coding agent on the planet.",
		strings.TrimSpace(ClaudeCodeSystemPrompt),
	)
	return text
}

func MarshalAnthropicSystemTextBlock(text string, includeCacheControl bool) ([]byte, error) {
	block := AnthropicSystemTextBlockPayload{
		Type: "text",
		Text: text,
	}
	if includeCacheControl {
		block.CacheControl = &AnthropicCacheControlPayload{
			Type: "ephemeral",
			TTL:  DefaultCacheControlTTL,
		}
	}
	return json.Marshal(block)
}

func MarshalAnthropicSystemTextBlockWithCacheControl(text string, cacheControl any) ([]byte, error) {
	block := map[string]any{
		"type": "text",
		"text": text,
	}
	if cacheControl != nil {
		block["cache_control"] = cacheControl
	}
	return json.Marshal(block)
}

func MarshalAnthropicMetadata(userID string) ([]byte, error) {
	return json.Marshal(AnthropicMetadataPayload{UserID: userID})
}

func BuildJSONArrayRaw(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}

	total := 2
	for _, item := range items {
		total += len(item)
	}
	total += len(items) - 1

	buf := make([]byte, 0, total)
	buf = append(buf, '[')
	for i, item := range items {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, item...)
	}
	buf = append(buf, ']')
	return buf
}

func SetJSONValueBytes(body []byte, path string, value any) ([]byte, bool) {
	next, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body, false
	}
	return next, true
}

func SetJSONRawBytes(body []byte, path string, raw []byte) ([]byte, bool) {
	next, err := sjson.SetRawBytes(body, path, raw)
	if err != nil {
		return body, false
	}
	return next, true
}

func DeleteJSONPathBytes(body []byte, path string) ([]byte, bool) {
	next, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return body, false
	}
	return next, true
}

func NormalizeClaudeOAuthSystemBody(body []byte, opts ClaudeOAuthNormalizeOptions) ([]byte, bool) {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return body, false
	}

	out := body
	modified := false

	switch {
	case sys.Type == gjson.String:
		sanitized := SanitizeSystemText(sys.String())
		if sanitized != sys.String() {
			if next, ok := SetJSONValueBytes(out, "system", sanitized); ok {
				out = next
				modified = true
			}
		}
	case sys.IsArray():
		index := 0
		sys.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "text" {
				textResult := item.Get("text")
				if textResult.Exists() && textResult.Type == gjson.String {
					text := textResult.String()
					sanitized := SanitizeSystemText(text)
					if sanitized != text {
						if next, ok := SetJSONValueBytes(out, fmt.Sprintf("system.%d.text", index), sanitized); ok {
							out = next
							modified = true
						}
					}
				}
			}

			if opts.StripSystemCacheControl && item.Get("cache_control").Exists() {
				if next, ok := DeleteJSONPathBytes(out, fmt.Sprintf("system.%d.cache_control", index)); ok {
					out = next
					modified = true
				}
			}

			index++
			return true
		})
	}

	return out, modified
}

func EnsureClaudeOAuthMetadataUserID(body []byte, userID string) ([]byte, bool) {
	if strings.TrimSpace(userID) == "" {
		return body, false
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		raw, err := MarshalAnthropicMetadata(userID)
		if err != nil {
			return body, false
		}
		return SetJSONRawBytes(body, "metadata", raw)
	}

	trimmedRaw := strings.TrimSpace(metadata.Raw)
	if strings.HasPrefix(trimmedRaw, "{") {
		existing := metadata.Get("user_id")
		if existing.Exists() && existing.Type == gjson.String && existing.String() != "" {
			return body, false
		}
		return SetJSONValueBytes(body, "metadata.user_id", userID)
	}

	raw, err := MarshalAnthropicMetadata(userID)
	if err != nil {
		return body, false
	}
	return SetJSONRawBytes(body, "metadata", raw)
}

// NormalizeClaudeOAuthRequestBody normalizes OAuth prompts, tools, caching, and context settings.
func NormalizeClaudeOAuthRequestBody(body []byte, opts ClaudeOAuthNormalizeOptions) []byte {
	if len(body) == 0 {
		return body
	}

	out := body
	modified := false

	if next, changed := NormalizeClaudeOAuthSystemBody(out, opts); changed {
		out = next
		modified = true
	}

	// Include an empty tools array when the client omits it.
	if !gjson.GetBytes(out, "tools").Exists() {
		if next, ok := SetJSONRawBytes(out, "tools", []byte("[]")); ok {
			out = next
			modified = true
		}
	}

	if opts.InjectMetadata && opts.MetadataUserID != "" {
		if next, changed := EnsureClaudeOAuthMetadataUserID(out, opts.MetadataUserID); changed {
			out = next
			modified = true
		}
	}

	// Use the CLI temperature default only when the client omits the field.
	if !gjson.GetBytes(out, "temperature").Exists() {
		if next, ok := SetJSONValueBytes(out, "temperature", 1); ok {
			out = next
			modified = true
		}
	}

	// Match the CLI max_tokens default when absent.
	if !gjson.GetBytes(out, "max_tokens").Exists() {
		if next, ok := SetJSONValueBytes(out, "max_tokens", 128000); ok {
			out = next
			modified = true
		}
	}

	// Enabled or adaptive thinking supplies the CLI context-management default.
	// SanitizeAnthropicBodyForBetaTokens later removes it if the final header lacks
	// the required beta, after provider overrides have been applied.
	if !gjson.GetBytes(out, "context_management").Exists() {
		thinkingType := gjson.GetBytes(out, "thinking.type").String()
		if thinkingType == "enabled" || thinkingType == "adaptive" {
			const cmDefault = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`
			if next, ok := SetJSONRawBytes(out, "context_management", []byte(cmDefault)); ok {
				out = next
				modified = true
			}
		}
	}

	// Keep tool_choice when tools exist. ApplyToolNameRewriteToBody maps named
	// choices alongside tool declarations; an empty tools list makes the choice unusable.
	if !gjson.GetBytes(out, "tools").IsArray() || len(gjson.GetBytes(out, "tools").Array()) == 0 {
		if gjson.GetBytes(out, "tool_choice").Exists() {
			if next, ok := DeleteJSONPathBytes(out, "tool_choice"); ok {
				out = next
				modified = true
			}
		}
	}

	if !modified {
		return body
	}

	return out
}

// BuildStableSessionSeed combines provider ID, client identity, and first user text.
// These anchors stay stable when turns are appended, allowing a stateless proxy
// to derive a repeatable metadata session UUID. Sticky routing hashes are separate.
func BuildStableSessionSeed(providerID int64, clientDiscriminator, firstUserText string) string {
	var b strings.Builder
	_, _ = b.WriteString(strconv.FormatInt(providerID, 10))
	_, _ = b.WriteString("::")
	_, _ = b.WriteString(clientDiscriminator)
	_, _ = b.WriteString("::")
	_, _ = b.WriteString(firstUserText)
	return b.String()
}

// NormalizeSystemParam decodes json.RawMessage so callers can switch on string,
// []any, or nil. Empty or invalid JSON becomes nil.
func NormalizeSystemParam(system any) any {
	raw, ok := system.(json.RawMessage)
	if !ok {
		return system
	}
	if len(raw) == 0 {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed
}

// SystemIncludesClaudeCodePrompt reports whether system contains a recognized
// Claude Code prefix, including Agent SDK variants.
func SystemIncludesClaudeCodePrompt(system any) bool {
	system = NormalizeSystemParam(system)
	switch v := system.(type) {
	case string:
		return HasClaudeCodePrefix(v)
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && HasClaudeCodePrefix(text) {
					return true
				}
			}
		}
	}
	return false
}

// HasClaudeCodePrefix reports whether text starts with a recognized Claude Code banner.
func HasClaudeCodePrefix(text string) bool {
	for _, prefix := range ClaudeCodePromptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// InjectClaudeCodePrompt prepends a Claude Code block to null, string, or array system content.
func InjectClaudeCodePrompt(body []byte, system any) []byte {
	system = NormalizeSystemParam(system)
	claudeCodeBlock, err := MarshalAnthropicSystemTextBlock(ClaudeCodeSystemPrompt, true)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "Warning: failed to build Claude Code prompt block: %v", err)
		return body
	}
	// Keep the identity in its own block and prefix the next instruction too,
	// so the identity remains visible when upstream concatenates system text.
	claudeCodePrefix := strings.TrimSpace(ClaudeCodeSystemPrompt)

	var items [][]byte

	switch v := system.(type) {
	case nil:
		items = [][]byte{claudeCodeBlock}
	case string:
		// Be tolerant of older/newer clients that may differ only by trailing whitespace/newlines.
		if strings.TrimSpace(v) == "" || strings.TrimSpace(v) == strings.TrimSpace(ClaudeCodeSystemPrompt) {
			items = [][]byte{claudeCodeBlock}
		} else {
			// Prefix the client instruction while retaining the separate identity block.
			merged := v
			if !strings.HasPrefix(v, claudeCodePrefix) {
				merged = claudeCodePrefix + "\n\n" + v
			}
			nextBlock, buildErr := MarshalAnthropicSystemTextBlock(merged, false)
			if buildErr != nil {
				logger.LegacyPrintf("service.gateway", "Warning: failed to build prefixed Claude Code system block: %v", buildErr)
				return body
			}
			items = [][]byte{claudeCodeBlock, nextBlock}
		}
	case []any:
		items = make([][]byte, 0, len(v)+1)
		items = append(items, claudeCodeBlock)
		prefixedNext := false
		systemResult := gjson.GetBytes(body, "system")
		if systemResult.IsArray() {
			systemResult.ForEach(func(_, item gjson.Result) bool {
				textResult := item.Get("text")
				if textResult.Exists() && textResult.Type == gjson.String &&
					strings.TrimSpace(textResult.String()) == strings.TrimSpace(ClaudeCodeSystemPrompt) {
					return true
				}

				raw := []byte(item.Raw)
				// Prefix the first subsequent text system block once.
				if !prefixedNext && item.Get("type").String() == "text" && textResult.Exists() && textResult.Type == gjson.String {
					text := textResult.String()
					if strings.TrimSpace(text) != "" && !strings.HasPrefix(text, claudeCodePrefix) {
						next, setErr := sjson.SetBytes(raw, "text", claudeCodePrefix+"\n\n"+text)
						if setErr == nil {
							raw = next
							prefixedNext = true
						}
					}
				}
				items = append(items, raw)
				return true
			})
		} else {
			for _, item := range v {
				m, ok := item.(map[string]any)
				if !ok {
					raw, marshalErr := json.Marshal(item)
					if marshalErr == nil {
						items = append(items, raw)
					}
					continue
				}
				if text, ok := m["text"].(string); ok && strings.TrimSpace(text) == strings.TrimSpace(ClaudeCodeSystemPrompt) {
					continue
				}
				if !prefixedNext {
					if blockType, _ := m["type"].(string); blockType == "text" {
						if text, ok := m["text"].(string); ok && strings.TrimSpace(text) != "" && !strings.HasPrefix(text, claudeCodePrefix) {
							m["text"] = claudeCodePrefix + "\n\n" + text
							prefixedNext = true
						}
					}
				}
				raw, marshalErr := json.Marshal(m)
				if marshalErr == nil {
					items = append(items, raw)
				}
			}
		}
	default:
		items = [][]byte{claudeCodeBlock}
	}

	result, ok := SetJSONRawBytes(body, "system", BuildJSONArrayRaw(items))
	if !ok {
		logger.LegacyPrintf("service.gateway", "Warning: failed to inject Claude Code prompt")
		return body
	}
	return result
}

func ClaudeOAuthSystemPromptBlocksForModel(model, configured string) string {
	if IsAnthropicFableModel(model) {
		return ClaudeFableOAuthSystemPromptBlocks
	}
	return configured
}

func DefaultClaudeOAuthExpansionPrompt(expansionPrompt string) string {
	expansionPrompt = strings.TrimSpace(expansionPrompt)
	if expansionPrompt == "" {
		return ClaudeCodeSystemPromptExpansion
	}
	return expansionPrompt
}

func ParseClaudeOAuthSystemPromptBlocksConfig(raw string) ([]ClaudeOAuthSystemPromptBlockConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var blocks []ClaudeOAuthSystemPromptBlockConfig
		if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
			return nil, err
		}
		return blocks, nil
	}
	var envelope ClaudeOAuthSystemPromptBlocksEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, err
	}
	return envelope.Blocks, nil
}

func DecodeClaudeOAuthSystemPromptCacheControl(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) {
		return nil, nil
	}
	if bytes.Equal(trimmed, []byte("true")) {
		return map[string]string{
			"type": "ephemeral",
			"ttl":  DefaultCacheControlTTL,
		}, nil
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("cache_control must be boolean, null, or object")
	}
	return value, nil
}

func ExpandClaudeOAuthSystemPromptTextTemplate(body []byte, text string, expansionPrompt string) (string, error) {
	if text == "" {
		return "", nil
	}
	expansionPrompt = DefaultClaudeOAuthExpansionPrompt(expansionPrompt)
	billingText, err := BuildBillingAttributionText(body, CLIVersion())
	if err != nil {
		return "", err
	}
	fp := ComputeClaudeCodeFingerprint(body, CLIVersion())
	replacer := strings.NewReplacer(
		"{billing_header}", billingText,
		"{cc_version}", CLIVersion(),
		"{fp}", fp,
		"{claude_code_system_prompt}", ClaudeCodeSystemPrompt,
		"{claude_code_expansion_prompt}", expansionPrompt,
	)
	return replacer.Replace(text), nil
}

func DefaultClaudeOAuthSystemPromptBlockConfig() []ClaudeOAuthSystemPromptBlockConfig {
	enabled := true
	return []ClaudeOAuthSystemPromptBlockConfig{
		{
			Enabled: &enabled,
			Type:    "text",
			Text:    "{billing_header}",
		},
		{
			Enabled: &enabled,
			Type:    "text",
			Text:    "{claude_code_system_prompt}",
		},
		{
			Enabled: &enabled,
			Type:    "text",
			Text:    "{claude_code_expansion_prompt}",
			CacheControl: json.RawMessage(
				fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, DefaultCacheControlTTL),
			),
		},
	}
}

func BuildClaudeOAuthSystemPromptBlocksJSON(body []byte, expansionPrompt string, blocksConfig string) ([][]byte, error) {
	blocks, err := ParseClaudeOAuthSystemPromptBlocksConfig(blocksConfig)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		blocks = DefaultClaudeOAuthSystemPromptBlockConfig()
	}

	items := make([][]byte, 0, len(blocks))
	for i, block := range blocks {
		if block.Enabled != nil && !*block.Enabled {
			continue
		}
		blockType := strings.TrimSpace(block.Type)
		if blockType == "" {
			blockType = "text"
		}
		if blockType != "text" {
			return nil, fmt.Errorf("system block %d type %q is not supported", i, block.Type)
		}
		text, err := ExpandClaudeOAuthSystemPromptTextTemplate(body, block.Text, expansionPrompt)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		cacheControl, err := DecodeClaudeOAuthSystemPromptCacheControl(block.CacheControl)
		if err != nil {
			return nil, fmt.Errorf("system block %d cache_control: %w", i, err)
		}
		raw, err := MarshalAnthropicSystemTextBlockWithCacheControl(text, cacheControl)
		if err != nil {
			return nil, err
		}
		items = append(items, raw)
	}
	return items, nil
}

func ValidateClaudeOAuthSystemPromptBlocksConfig(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	blocks, err := ParseClaudeOAuthSystemPromptBlocksConfig(raw)
	if err != nil {
		return infraerrors.BadRequest("INVALID_CLAUDE_OAUTH_SYSTEM_PROMPT_BLOCKS", "claude oauth system prompt blocks must be valid JSON")
	}
	for i, block := range blocks {
		blockType := strings.TrimSpace(block.Type)
		if blockType == "" {
			blockType = "text"
		}
		if blockType != "text" {
			return infraerrors.BadRequest("INVALID_CLAUDE_OAUTH_SYSTEM_PROMPT_BLOCKS", fmt.Sprintf("system block %d type must be text", i))
		}
		if _, err := DecodeClaudeOAuthSystemPromptCacheControl(block.CacheControl); err != nil {
			return infraerrors.BadRequest("INVALID_CLAUDE_OAUTH_SYSTEM_PROMPT_BLOCKS", fmt.Sprintf("system block %d cache_control is invalid", i))
		}
	}
	return nil
}

func ExtractSystemTextAndCacheControl(system any) (string, any) {
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v), nil
	case []any:
		var parts []string
		var cacheControl any
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, ok := m["text"].(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			parts = append(parts, text)
			// The system text is merged into one message block. Keep the last cache
			// breakpoint and its TTL as the closest available representation.
			if cc, exists := m["cache_control"]; exists && cc != nil {
				cacheControl = cc
			}
		}
		return strings.Join(parts, "\n\n"), cacheControl
	default:
		return "", nil
	}
}

func RewriteSystemForNonClaudeCodeWithPromptBlocks(body []byte, system any, expansionPrompt string, blocksConfig string) []byte {
	system = NormalizeSystemParam(system)
	expansionPrompt = DefaultClaudeOAuthExpansionPrompt(expansionPrompt)

	originalSystemText, originalSystemCacheControl := ExtractSystemTextAndCacheControl(system)

	// Build the configured system blocks. Defaults contain billing attribution,
	// the Claude Code identity, and a tool-independent expansion with a cache breakpoint.
	systemBlocks, blockErr := BuildClaudeOAuthSystemPromptBlocksJSON(body, expansionPrompt, blocksConfig)
	if blockErr != nil {
		logger.LegacyPrintf("service.gateway", "Warning: failed to build configured Claude OAuth system blocks: %v", blockErr)
		systemBlocks, blockErr = BuildClaudeOAuthSystemPromptBlocksJSON(body, expansionPrompt, "")
	}
	if blockErr != nil {
		logger.LegacyPrintf("service.gateway", "Warning: failed to build default Claude OAuth system blocks: %v", blockErr)
		return body
	}
	out, ok := SetJSONRawBytes(body, "system", BuildJSONArrayRaw(systemBlocks))
	if !ok {
		logger.LegacyPrintf("service.gateway", "Warning: failed to set Claude Code system prompt")
		return body
	}

	// Move client system instructions into a user/assistant pair so they remain
	// available after the system field is replaced.
	ccPromptTrimmed := strings.TrimSpace(ClaudeCodeSystemPrompt)
	if originalSystemText != "" && originalSystemText != ccPromptTrimmed && !HasClaudeCodePrefix(originalSystemText) {
		instructionBlock := map[string]any{
			"type": "text",
			"text": "[System Instructions]\n" + originalSystemText,
		}
		if originalSystemCacheControl != nil {
			instructionBlock["cache_control"] = originalSystemCacheControl
		}
		instrMsg, err1 := json.Marshal(map[string]any{
			"role": "user",
			"content": []map[string]any{
				instructionBlock,
			},
		})
		ackMsg, err2 := json.Marshal(map[string]any{
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "Understood. I will follow these instructions."},
			},
		})
		if err1 != nil || err2 != nil {
			logger.LegacyPrintf("service.gateway", "Warning: failed to marshal system-to-messages injection")
			return out
		}

		items := [][]byte{instrMsg, ackMsg}
		messagesResult := gjson.GetBytes(out, "messages")
		if messagesResult.IsArray() {
			messagesResult.ForEach(func(_, msg gjson.Result) bool {
				items = append(items, []byte(msg.Raw))
				return true
			})
		}

		if next, setOk := SetJSONRawBytes(out, "messages", BuildJSONArrayRaw(items)); setOk {
			out = next
		}
	}

	return out
}

func CollectCacheControlPaths(body []byte) (invalidThinking []CacheControlPath, messagePaths []string, toolPaths []string, systemPaths []string) {
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		sysIndex := 0
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				path := fmt.Sprintf("system.%d.cache_control", sysIndex)
				if item.Get("type").String() == "thinking" {
					invalidThinking = append(invalidThinking, CacheControlPath{
						Path: path,
						Log:  "[Warning] Removed illegal cache_control from thinking block in system",
					})
				} else {
					systemPaths = append(systemPaths, path)
				}
			}
			sysIndex++
			return true
		})
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		msgIndex := 0
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				contentIndex := 0
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
						path := fmt.Sprintf("messages.%d.content.%d.cache_control", msgIndex, contentIndex)
						if item.Get("type").String() == "thinking" {
							invalidThinking = append(invalidThinking, CacheControlPath{
								Path: path,
								Log:  fmt.Sprintf("[Warning] Removed illegal cache_control from thinking block in messages[%d].content[%d]", msgIndex, contentIndex),
							})
						} else {
							messagePaths = append(messagePaths, path)
						}
					}
					contentIndex++
					return true
				})
			}
			msgIndex++
			return true
		})
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		toolIndex := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("cache_control").Exists() {
				toolPaths = append(toolPaths, fmt.Sprintf("tools.%d.cache_control", toolIndex))
			}
			toolIndex++
			return true
		})
	}

	return invalidThinking, messagePaths, toolPaths, systemPaths
}

// EnforceCacheControlLimit removes invalid thinking breakpoints and limits the rest to four.
// It removes tool breakpoints first, then message breakpoints, then system breakpoints.
func EnforceCacheControlLimit(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	invalidThinking, messagePaths, toolPaths, systemPaths := CollectCacheControlPaths(body)
	out := body
	modified := false

	// Thinking blocks do not support cache_control.
	for _, item := range invalidThinking {
		if !gjson.GetBytes(out, item.Path).Exists() {
			continue
		}
		next, ok := DeleteJSONPathBytes(out, item.Path)
		if !ok {
			continue
		}
		out = next
		modified = true
		logger.LegacyPrintf("service.gateway", "%s", item.Log)
	}

	count := len(messagePaths) + len(toolPaths) + len(systemPaths)
	if count <= MaxCacheControlBlocks {
		if modified {
			return out
		}
		return body
	}

	remaining := count - MaxCacheControlBlocks
	for i := len(toolPaths) - 1; i >= 0 && remaining > 0; i-- {
		path := toolPaths[i]
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		next, ok := DeleteJSONPathBytes(out, path)
		if !ok {
			continue
		}
		out = next
		modified = true
		remaining--
	}

	for _, path := range messagePaths {
		if remaining <= 0 {
			break
		}
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		next, ok := DeleteJSONPathBytes(out, path)
		if !ok {
			continue
		}
		out = next
		modified = true
		remaining--
	}

	for i := len(systemPaths) - 1; i >= 0 && remaining > 0; i-- {
		path := systemPaths[i]
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		next, ok := DeleteJSONPathBytes(out, path)
		if !ok {
			continue
		}
		out = next
		modified = true
		remaining--
	}

	if modified {
		return out
	}
	return body
}

// InjectAnthropicCacheControlTTL1h sets existing ephemeral cache breakpoints to one hour.
func InjectAnthropicCacheControlTTL1h(body []byte) []byte {
	return ForceEphemeralCacheControlTTL(body, CacheTTLTarget1h)
}

func ForceEphemeralCacheControlTTL(body []byte, ttl string) []byte {
	if len(body) == 0 || ttl == "" {
		return body
	}
	out := body
	var paths []string
	addPath := func(path string, value gjson.Result) {
		cc := value.Get("cache_control")
		if !cc.Exists() || cc.Get("type").String() != "ephemeral" {
			return
		}
		if cc.Get("ttl").String() == ttl {
			return
		}
		paths = append(paths, path+".cache_control.ttl")
	}

	if topCC := gjson.GetBytes(body, "cache_control"); topCC.Exists() && topCC.Get("type").String() == "ephemeral" && topCC.Get("ttl").String() != ttl {
		paths = append(paths, "cache_control.ttl")
	}

	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		idx := -1
		system.ForEach(func(_, block gjson.Result) bool {
			idx++
			addPath(fmt.Sprintf("system.%d", idx), block)
			return true
		})
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		msgIdx := -1
		messages.ForEach(func(_, msg gjson.Result) bool {
			msgIdx++
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			contentIdx := -1
			content.ForEach(func(_, block gjson.Result) bool {
				contentIdx++
				addPath(fmt.Sprintf("messages.%d.content.%d", msgIdx, contentIdx), block)
				return true
			})
			return true
		})
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		idx := -1
		tools.ForEach(func(_, tool gjson.Result) bool {
			idx++
			addPath(fmt.Sprintf("tools.%d", idx), tool)
			return true
		})
	}

	for _, path := range paths {
		if next, err := sjson.SetBytes(out, path, ttl); err == nil {
			out = next
		}
	}
	return out
}

// SystemHasClaudeCodeBillingAttribution recognizes a CLI billing marker in system.
// It detects proxied client traffic; it is not a ClaudeCodeOnly authorization check.
func SystemHasClaudeCodeBillingAttribution(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return false
	}
	found := false
	system.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text").String()
		if strings.HasPrefix(text, ClaudeCodeBillingHeaderPrefix+":") && strings.Contains(text, ClaudeCodeEntrypointMarker) {
			found = true
			return false
		}
		return true
	})
	return found
}

// IsProxiedClaudeCodeRequest requires both valid metadata and a CLI billing marker.
func IsProxiedClaudeCodeRequest(body []byte, metadataUserID string) bool {
	return ParseMetadataUserID(metadataUserID) != nil && SystemHasClaudeCodeBillingAttribution(body)
}
