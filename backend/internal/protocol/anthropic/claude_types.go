package anthropic

import (
	"encoding/json"
)

const (
	// ClaudeCodeBillingHeaderPrefix identifies billing-attribution text in the request.
	ClaudeCodeBillingHeaderPrefix = "x-anthropic-billing-header"

	// ClaudeCodeEntrypointMarker identifies the entrypoint field in request text.
	ClaudeCodeEntrypointMarker = "cc_entrypoint="
)

// ClaudeRequest is a Claude Messages API request.
type ClaudeRequest struct {
	Model        string                 `json:"model"`
	Messages     []ClaudeMessage        `json:"messages"`
	MaxTokens    int                    `json:"max_tokens,omitempty"`
	System       json.RawMessage        `json:"system,omitempty"` // string or []SystemBlock
	Stream       bool                   `json:"stream,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	TopP         *float64               `json:"top_p,omitempty"`
	TopK         *int                   `json:"top_k,omitempty"`
	Tools        []ClaudeTool           `json:"tools,omitempty"`
	Thinking     *ThinkingConfig        `json:"thinking,omitempty"`
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
	Metadata     *ClaudeMetadata        `json:"metadata,omitempty"`
}

// ClaudeMessage is a message in a Claude conversation.
type ClaudeMessage struct {
	Role    string          `json:"role"` // user, assistant
	Content json.RawMessage `json:"content"`
}

// ThinkingConfig configures Claude's thinking mode and token budget.
type ThinkingConfig struct {
	Type         string `json:"type"`                    // "enabled" / "adaptive" / "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // thinking budget
}

// ClaudeMetadata contains request metadata.
type ClaudeMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// ClaudeTool represents a standard tool or an MCP custom tool.
// Standard tools use Description and InputSchema; MCP custom tools set Type to "custom" and use Custom.
type ClaudeTool struct {
	Type        string          `json:"type,omitempty"` // "custom" or empty for the standard form
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`  // standard form
	InputSchema map[string]any  `json:"input_schema,omitempty"` // standard form
	Custom      *CustomToolSpec `json:"custom,omitempty"`       // custom form
}

// CustomToolSpec contains an MCP custom tool's description and schema.
type CustomToolSpec struct {
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// ClaudeCustomToolSpec is the legacy name for CustomToolSpec.
type ClaudeCustomToolSpec = CustomToolSpec

// SystemBlock is an element of an array-valued system prompt.
type SystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ContentBlock is a decoded Claude message content block.
type ContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// tool_use
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// image
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource contains an encoded Claude image.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png" or "image/jpeg"
	Data      string `json:"data"`
}

// ClaudeResponse is a Claude Messages API response.
type ClaudeResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"` // "message"
	Role         string              `json:"role"` // "assistant"
	Model        string              `json:"model"`
	Content      []ClaudeContentItem `json:"content"`
	StopReason   string              `json:"stop_reason,omitempty"`   // end_turn, tool_use, max_tokens
	StopSequence *string             `json:"stop_sequence,omitempty"` // null or a stop sequence
	Usage        ClaudeUsage         `json:"usage"`
}

// ClaudeContentItem is a content item in a Claude response.
type ClaudeContentItem struct {
	Type string `json:"type"` // text, thinking, tool_use

	// text
	Text string `json:"text,omitempty"`

	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// ClaudeUsage contains Claude token counts.
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	ImageOutputTokens        int `json:"image_output_tokens,omitempty"`
}

// ClaudeError is a Claude error response.
type ClaudeError struct {
	Type  string      `json:"type"` // "error"
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the upstream error type and message.
type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ClaudeModel describes a model returned by the Claude API.
type ClaudeModel struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}
