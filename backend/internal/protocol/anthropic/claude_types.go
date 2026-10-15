package anthropic

import (
	"encoding/json"
)

// claude_types.go defines Claude request and response shapes used by
// conversation adapters.
// Claude conversation adapters retain raw JSON for polymorphic system and
// message content while decoding generation settings and tool declarations.
// Standard and custom tools place their schemas at different JSON locations,
// which the Gemini bridge resolves before adapting those mutable schemas to its
// supported input format.

// These markers identify Claude Code billing attribution and entrypoint text
// when request fingerprints and client-validation policies inspect system prompts.
const (
	// ClaudeCodeBillingHeaderPrefix identifies billing-attribution text in the request.
	ClaudeCodeBillingHeaderPrefix = "x-anthropic-billing-header"

	// ClaudeCodeEntrypointMarker identifies the entrypoint field in request text.
	ClaudeCodeEntrypointMarker = "cc_entrypoint="
)

// ClaudeRequest represents Messages input consumed by Claude-to-Gemini conversation adapters.
type ClaudeRequest struct {
	Model string `json:"model"` // forwarded model identifier
	// Messages shares its slice with copies of ClaudeRequest. Signature-repair
	// callers copy this slice before mutating history for an independent retry.
	Messages  []ClaudeMessage `json:"messages"`             // ordered conversation turns
	MaxTokens int             `json:"max_tokens,omitempty"` // maximum generated tokens
	System    json.RawMessage `json:"system,omitempty"`     // JSON string or system-block array
	Stream    bool            `json:"stream,omitempty"`     // streaming response selection
	// Temperature, TopP, and TopK distinguish omitted settings from supplied
	// zero values. Nil pointers leave the corresponding generation setting for
	// the downstream adapter or upstream to choose.
	Temperature  *float64               `json:"temperature,omitempty"`   // optional sampling temperature
	TopP         *float64               `json:"top_p,omitempty"`         // optional nucleus probability
	TopK         *int                   `json:"top_k,omitempty"`         // optional sampling candidate-count limit
	Tools        []ClaudeTool           `json:"tools,omitempty"`         // advertised tool declarations
	Thinking     *ThinkingConfig        `json:"thinking,omitempty"`      // optional reasoning configuration
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"` // optional output configuration
	Metadata     *ClaudeMetadata        `json:"metadata,omitempty"`      // optional client metadata
}

// ClaudeMessage retains a conversation role and polymorphic JSON message content.
type ClaudeMessage struct {
	Role    string          `json:"role"`    // conversation message role
	Content json.RawMessage `json:"content"` // JSON string or block array
}

// ThinkingConfig configures Claude's thinking mode and token budget.
type ThinkingConfig struct {
	Type         string `json:"type"`                    // "enabled", "adaptive", or "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // maximum reasoning tokens
}

// ClaudeMetadata holds the client identity text attached to a Messages request.
type ClaudeMetadata struct {
	UserID string `json:"user_id,omitempty"` // encoded client/session identity
}

// ClaudeTool declares function, server, or nested custom tools for conversation adapters.
type ClaudeTool struct {
	// For function declarations, Type selects the schema location: Description
	// and InputSchema describe ordinary tools, while Type "custom" directs the
	// Gemini bridge to Custom.Description and Custom.InputSchema. Name identifies
	// either form; built-in server tools are recognized before schema adaptation.
	Type        string `json:"type,omitempty"`        // tool representation discriminator
	Name        string `json:"name"`                  // advertised tool name
	Description string `json:"description,omitempty"` // advertised tool description
	// InputSchema is cleaned in place by BuildInternalGeminiTools for standard
	// tools; callers copy mutable schema data before adapting a shared declaration.
	InputSchema map[string]any `json:"input_schema,omitempty"` // JSON Schema object
	// Custom supplies the nested function specification for Type "custom".
	// BuildInternalGeminiTools skips that declaration when Custom or its schema is nil.
	Custom *CustomToolSpec `json:"custom,omitempty"` // nested custom-tool specification
}

// CustomToolSpec contains the description and schema of a nested custom tool.
type CustomToolSpec struct {
	Description string `json:"description,omitempty"` // advertised tool description
	// InputSchema is cleaned in place when BuildInternalGeminiTools adapts a
	// custom tool; callers copy mutable schema data before sharing this specification.
	InputSchema map[string]any `json:"input_schema"` // JSON Schema object
}

// ClaudeCustomToolSpec is an alias for the same nested custom tool specification.
type ClaudeCustomToolSpec = CustomToolSpec

// SystemBlock is an element of an array-valued system prompt.
type SystemBlock struct {
	Type string `json:"type"` // wire block discriminator
	Text string `json:"text"` // readable block text
}

// ContentBlock holds the decoded fields of a polymorphic Claude message block.
type ContentBlock struct {
	Type string `json:"type"` // wire block discriminator
	// Text supplies readable content when Type is text. Empty strings are
	// omitted by the standard JSON encoder for this request-block representation.
	Text string `json:"text,omitempty"` // readable block text
	// Thinking and Signature describe a reasoning block when Type is thinking.
	// The readable reasoning and its opaque replay signature remain separate
	// fields for adapters that handle signature validation independently.
	Thinking  string `json:"thinking,omitempty"`  // readable reasoning text
	Signature string `json:"signature,omitempty"` // opaque reasoning signature
	// ID, Name, and Input identify a tool invocation when Type is tool_use.
	// The identifier links later tool results to this call, while Input retains
	// the decoded arguments supplied to the named tool.
	ID    string `json:"id,omitempty"`    // tool invocation identifier
	Name  string `json:"name,omitempty"`  // invoked tool name
	Input any    `json:"input,omitempty"` // decoded tool arguments
	// ToolUseID, Content, and IsError describe a tool_result block together.
	// ToolUseID links to the earlier call's ID, Content holds its output, and
	// IsError reports whether the tool marked that execution as failed.
	ToolUseID string          `json:"tool_use_id,omitempty"` // originating tool-call identifier
	Content   json.RawMessage `json:"content,omitempty"`     // JSON result content
	IsError   bool            `json:"is_error,omitempty"`    // tool execution failure
	// Source supplies encoded image data when Type is image. A nil source is
	// omitted by JSON serialization; adapters validate whether the block is usable.
	Source *ImageSource `json:"source,omitempty"` // encoded image source
}

// ImageSource holds the MIME type and encoded data for a Claude image.
type ImageSource struct {
	Type      string `json:"type"`       // base64 source encoding
	MediaType string `json:"media_type"` // image MIME type
	Data      string `json:"data"`       // base64
}

// ClaudeResponse holds a buffered Messages result produced by Claude conversation adapters.
type ClaudeResponse struct {
	ID      string              `json:"id"`      // response message identifier
	Type    string              `json:"type"`    // message response type
	Role    string              `json:"role"`    // assistant response role
	Model   string              `json:"model"`   // client-visible model identifier
	Content []ClaudeContentItem `json:"content"` // ordered output blocks
	// StopReason identifies why generation ended, such as end_turn, tool_use,
	// or max_tokens. StopSequence identifies a matched stopping string; an
	// empty reason and a nil sequence are omitted during JSON encoding.
	StopReason   string      `json:"stop_reason,omitempty"`   // generation stop reason
	StopSequence *string     `json:"stop_sequence,omitempty"` // matched stopping sequence
	Usage        ClaudeUsage `json:"usage"`                   // reported token buckets
}

// ClaudeContentItem is a content item in a Claude response.
type ClaudeContentItem struct {
	Type string `json:"type"` // wire block discriminator

	// Text supplies readable output when Type is text. Empty strings are
	// omitted by the standard JSON encoder for this buffered-response representation.
	Text string `json:"text,omitempty"` // readable block text

	// Thinking and Signature describe reasoning output when Type is thinking.
	// Readable reasoning and the opaque signature are separate fields so clients
	// can display the text while retaining signature data for later replay.
	Thinking  string `json:"thinking,omitempty"`  // readable reasoning text
	Signature string `json:"signature,omitempty"` // opaque reasoning signature

	// ID, Name, and Input describe a generated tool call when Type is tool_use.
	// ID links the client's later result to this invocation, while Input holds
	// the decoded arguments for the named tool.
	ID    string `json:"id,omitempty"`    // tool invocation identifier
	Name  string `json:"name,omitempty"`  // invoked tool name
	Input any    `json:"input,omitempty"` // decoded tool arguments
}

// ClaudeUsage holds token buckets reported by converted Claude Messages responses.
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`                          // uncached prompt tokens
	OutputTokens             int `json:"output_tokens"`                         // generated response tokens
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"` // cache-written prompt tokens
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`     // cache-read prompt tokens
	ImageOutputTokens        int `json:"image_output_tokens,omitempty"`         // generated image tokens
}

// ClaudeError wraps an upstream error type and its diagnostic detail.
type ClaudeError struct {
	Type  string      `json:"type"`  // error response type
	Error ErrorDetail `json:"error"` // upstream diagnostic detail
}

// ErrorDetail contains the upstream error type and message.
type ErrorDetail struct {
	Type    string `json:"type"`    // upstream error category
	Message string `json:"message"` // upstream diagnostic message
}

// ClaudeModel describes a model returned by the Claude API.
type ClaudeModel struct {
	ID          string `json:"id"`           // upstream model identifier
	Type        string `json:"type"`         // catalogue object type
	DisplayName string `json:"display_name"` // upstream display name
	CreatedAt   string `json:"created_at"`   // upstream creation timestamp
}
