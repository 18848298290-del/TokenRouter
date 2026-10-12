package anthropic

import (
	"encoding/json"
)

// AnthropicRequest is the request body for POST /v1/messages.
type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    json.RawMessage    `json:"system,omitempty"` // string or []AnthropicContentBlock
	Messages  []AnthropicMessage `json:"messages"`
	Tools     []AnthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
	// Speed requests Claude Fast mode with "fast".
	Speed       string             `json:"speed,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	StopSeqs    []string           `json:"stop_sequences,omitempty"`
	Thinking    *AnthropicThinking `json:"thinking,omitempty"`
	ToolChoice  json.RawMessage    `json:"tool_choice,omitempty"`
	// Metadata survives request reserialization so the OAuth/Claude Code path can
	// rewrite metadata.user_id before forwarding. Upstream client attribution
	// depends on that value; dropping metadata here removes the input needed by
	// ensureClaudeOAuthMetadataUserID and RewriteUserIDWithMasking.
	// Keep the raw object, including fields this type does not interpret.
	Metadata     json.RawMessage        `json:"metadata,omitempty"`
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
}

// AnthropicOutputConfig controls output generation parameters.
type AnthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"` // "low" | "medium" | "high" | "max"
}

// AnthropicThinking configures extended thinking in the Anthropic API.
type AnthropicThinking struct {
	Type         string `json:"type"`                    // "enabled" | "adaptive" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // max thinking tokens
}

// AnthropicMessage is a single message in the Anthropic conversation.
type AnthropicMessage struct {
	Role    string          `json:"role"` // "user" | "assistant"
	Content json.RawMessage `json:"content"`
}

// AnthropicContentBlock is one block inside a message's content array.
type AnthropicContentBlock struct {
	Type string `json:"type"`

	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=thinking
	Thinking string `json:"thinking,omitempty"`
	// Signature carries upstream encrypted reasoning, such as xAI encrypted_content,
	// for Claude clients to replay unchanged in later turns.
	Signature string `json:"signature,omitempty"`

	// type=image
	Source *AnthropicImageSource `json:"source,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or []AnthropicContentBlock
	IsError   bool            `json:"is_error,omitempty"`
}

// AnthropicImageSource describes the source data for an image content block.
type AnthropicImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// AnthropicTool describes a tool available to the model.
type AnthropicTool struct {
	Type         string                 `json:"type,omitempty"` // e.g. "web_search_20250305" for server tools
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema,omitempty"` // JSON Schema object
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicCacheControl represents the API's cache_control field.
// The caller chooses a TTL; see claude.DefaultCacheControlTTL for the project default.
type AnthropicCacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "5m" / "1h"; omission uses the Anthropic default of 5m
}

// AnthropicResponse is a Messages response, also embedded in message_start events.
//
// StopReason is a pointer so message_start can serialize JSON null.
// An empty string is not a valid intermediate stop reason for strict clients.
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"` // "message"
	Role         string                  `json:"role"` // "assistant"
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicPromptTokensDetails contains OpenAI-style prompt details returned
// by some Anthropic-compatible providers.
type AnthropicPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// AnthropicUsage holds token counts in Anthropic format.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// Speed is the actual Claude processing speed: fast or standard.
	Speed string `json:"speed,omitempty"`
	// Compatible providers may report OpenAI-style total and cached input counts.
	// Callers normalize these fields into mutually exclusive billing buckets.
	PromptTokens          int                           `json:"prompt_tokens,omitempty"`
	CachedTokens          int                           `json:"cached_tokens,omitempty"`
	PromptTokensDetails   *AnthropicPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens  *int                          `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens *int                          `json:"prompt_cache_miss_tokens,omitempty"`
}

// AnthropicStreamEvent is a single SSE event in the Anthropic streaming protocol.
type AnthropicStreamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *AnthropicResponse `json:"message,omitempty"`

	// content_block_start
	Index        *int                   `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`

	// content_block_delta
	Delta *AnthropicDelta `json:"delta,omitempty"`

	// message_delta
	Usage *AnthropicUsage `json:"usage,omitempty"`
}

// AnthropicDelta carries incremental content in streaming events.
type AnthropicDelta struct {
	Type string `json:"type,omitempty"` // "text_delta" | "input_json_delta" | "thinking_delta" | "signature_delta"

	// text_delta
	Text string `json:"text,omitempty"`

	// input_json_delta
	PartialJSON string `json:"partial_json,omitempty"`

	// thinking_delta
	Thinking string `json:"thinking,omitempty"`

	// signature_delta
	Signature string `json:"signature,omitempty"`

	// message_delta fields
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// MarshalJSON retains empty text/thinking fields required by content_block_start.
func (b AnthropicContentBlock) MarshalJSON() ([]byte, error) {
	type anthropicContentBlock AnthropicContentBlock

	switch b.Type {
	case "text":
		return json.Marshal(struct {
			Text string `json:"text"`
			anthropicContentBlock
		}{
			Text:                  b.Text,
			anthropicContentBlock: anthropicContentBlock(b),
		})
	case "thinking":
		return json.Marshal(struct {
			Thinking string `json:"thinking"`
			anthropicContentBlock
		}{
			Thinking:              b.Thinking,
			anthropicContentBlock: anthropicContentBlock(b),
		})
	default:
		return json.Marshal(anthropicContentBlock(b))
	}
}

// AnthropicStopReasonPtr returns a pointer to s, including when s is empty.
func AnthropicStopReasonPtr(s string) *string {
	return &s
}

// AnthropicStopReasonString returns the stop reason, or an empty string for nil.
func AnthropicStopReasonString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
