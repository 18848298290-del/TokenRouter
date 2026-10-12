package openai

import (
	"bytes"
	"encoding/json"
)

const (
	TextProtocolChatCompletions TextProtocol = "chat_completions"
	TextProtocolResponses       TextProtocol = "responses"
)

// ResponsesRequest is the request body for POST /v1/responses.
type ResponsesRequest struct {
	Model              string              `json:"model"`
	Instructions       string              `json:"instructions,omitempty"`
	Input              json.RawMessage     `json:"input"` // string or []ResponsesInputItem
	MaxOutputTokens    *int                `json:"max_output_tokens,omitempty"`
	Temperature        *float64            `json:"temperature,omitempty"`
	TopP               *float64            `json:"top_p,omitempty"`
	Stream             bool                `json:"stream,omitempty"`
	Tools              []ResponsesTool     `json:"tools,omitempty"`
	Include            []string            `json:"include,omitempty"`
	Store              *bool               `json:"store,omitempty"`
	ParallelToolCalls  *bool               `json:"parallel_tool_calls,omitempty"`
	Reasoning          *ResponsesReasoning `json:"reasoning,omitempty"`
	Text               *ResponsesText      `json:"text,omitempty"`
	ToolChoice         json.RawMessage     `json:"tool_choice,omitempty"`
	ServiceTier        string              `json:"service_tier,omitempty"`
	PromptCacheKey     string              `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string              `json:"previous_response_id,omitempty"`
}

// ResponsesReasoning configures reasoning effort in the Responses API.
type ResponsesReasoning struct {
	Effort  string `json:"effort"`            // "low" | "medium" | "high" | "xhigh" | "max"
	Summary string `json:"summary,omitempty"` // "auto" | "concise" | "detailed"
}

// ResponsesText configures text output options in the Responses API.
type ResponsesText struct {
	Format    json.RawMessage `json:"format,omitempty"`    // Responses structured output format
	Verbosity string          `json:"verbosity,omitempty"` // "low" | "medium" | "high"
}

// ResponsesInputItem is one item in the Responses API input array.
// The Type field determines which other fields are populated.
type ResponsesInputItem struct {
	// Common
	Type string `json:"type,omitempty"` // "" for role-based messages

	// Role-based messages (developer/system/user/assistant)
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"` // string or []ResponsesContentPart

	// type=reasoning: encrypted reasoning replayed across turns
	EncryptedContent string `json:"encrypted_content,omitempty"`

	// type=function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	ID        string `json:"id,omitempty"`

	// type=function_call_output
	Output    string `json:"output,omitempty"`
	outputRaw json.RawMessage
}

// ResponsesContentPart is a typed content part in a Responses message.
type ResponsesContentPart struct {
	Type     string `json:"type"` // input_text, output_text, input_image, or input_file
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"` // data URI for input_image

	// Fields for input_file parts.
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"` // file content encoded as a data URI
	FileID   string `json:"file_id,omitempty"`
}

// ResponsesTool describes a tool in the Responses API.
type ResponsesTool struct {
	Type        string          `json:"type"` // "function" | "custom" | "web_search" | "x_search" | "local_shell" etc.
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`

	// Namespace child tools; tools and children are alternate names, so callers supply one.
	Tools    []ResponsesTool `json:"tools,omitempty"`
	Children []ResponsesTool `json:"children,omitempty"`

	// type=x_search
	AllowedXHandles          []string `json:"allowed_x_handles,omitempty"`
	ExcludedXHandles         []string `json:"excluded_x_handles,omitempty"`
	FromDate                 string   `json:"from_date,omitempty"`
	ToDate                   string   `json:"to_date,omitempty"`
	EnableImageUnderstanding *bool    `json:"enable_image_understanding,omitempty"`
	EnableVideoUnderstanding *bool    `json:"enable_video_understanding,omitempty"`
}

// ResponsesResponse is the non-streaming response from POST /v1/responses.
type ResponsesResponse struct {
	ID     string `json:"id"`
	Object string `json:"object"` // "response"
	// CreatedAt is the Unix creation time in seconds. Strict Responses clients require
	// created_at even when zero; omitting it causes a missing-field decoding error.
	CreatedAt   int64             `json:"created_at"`
	Model       string            `json:"model"`
	Status      string            `json:"status"` // "completed" | "incomplete" | "failed"
	Output      []ResponsesOutput `json:"output"`
	Usage       *ResponsesUsage   `json:"usage,omitempty"`
	ServiceTier string            `json:"service_tier,omitempty"` // upstream service tier echoed to the client

	// incomplete_details is present when status="incomplete"
	IncompleteDetails *ResponsesIncompleteDetails `json:"incomplete_details,omitempty"`

	// Error is present when status="failed"
	Error *ResponsesError `json:"error,omitempty"`
}

// ResponsesError describes an error in a failed response.
type ResponsesError struct {
	Code       string `json:"code"`
	Type       string `json:"type,omitempty"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code,omitempty"` // upstream status code used by provider error policies
}

// ResponsesIncompleteDetails explains why a response is incomplete.
type ResponsesIncompleteDetails struct {
	Reason string `json:"reason"` // "max_output_tokens" | "content_filter"
}

// ResponsesOutput is one output item in a Responses API response.
type ResponsesOutput struct {
	Type string `json:"type"` // "message" | "reasoning" | "function_call" | "web_search_call"

	// type=message
	ID      string                 `json:"id,omitempty"`
	Role    string                 `json:"role,omitempty"`
	Content []ResponsesContentPart `json:"content,omitempty"`
	Status  string                 `json:"status,omitempty"`

	// type=reasoning
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Summary          []ResponsesSummary `json:"summary,omitempty"`

	// type=function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// Namespace containing the tool; Codex routes by namespace and name.
	Namespace string `json:"namespace,omitempty"`

	// type=custom_tool_call: free-form tool input
	Input string `json:"input,omitempty"`

	// type=web_search_call
	Action *WebSearchAction `json:"action,omitempty"`
}

// WebSearchAction describes the search action in a web_search_call output item.
type WebSearchAction struct {
	Type  string `json:"type,omitempty"`  // "search"
	Query string `json:"query,omitempty"` // primary search query
}

// ResponsesSummary is a summary text block inside a reasoning output.
type ResponsesSummary struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
}

// ResponsesUsage holds token counts in Responses API format.
type ResponsesUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	TotalTokens              int `json:"total_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`

	// Optional detailed breakdown
	InputTokensDetails  *ResponsesInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *ResponsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

// ResponsesInputTokensDetails breaks down input token usage.
type ResponsesInputTokensDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	AudioTokens         int `json:"audio_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
}

// ResponsesOutputTokensDetails breaks down output token usage.
type ResponsesOutputTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// ResponsesStreamEvent is a single SSE event in the Responses streaming protocol.
// The Type field corresponds to the "type" in the JSON payload.
type ResponsesStreamEvent struct {
	Type string `json:"type"`

	// response.created / response.completed / response.done / response.failed / response.incomplete
	Response *ResponsesResponse `json:"response,omitempty"`
	// Some compatible upstreams put usage at the top level of terminal events.
	Usage *ResponsesUsage `json:"usage,omitempty"`

	// response.output_item.added / response.output_item.done
	Item *ResponsesOutput `json:"item,omitempty"`

	// response.output_text.delta / response.output_text.done
	OutputIndex  int    `json:"output_index,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
	Delta        string `json:"delta,omitempty"`
	Text         string `json:"text,omitempty"`
	ItemID       string `json:"item_id,omitempty"`

	// response.function_call_arguments.delta / done
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// response.custom_tool_call_input.done
	Input string `json:"input,omitempty"`

	// response.reasoning_summary_text.delta / done
	// Reuses Text and Delta; SummaryIndex identifies the summary part.
	SummaryIndex int `json:"summary_index,omitempty"`

	// response.content_part.added / done and
	// response.reasoning_summary_part.added / done
	Part *ResponsesContentPart `json:"part,omitempty"`

	// error event fields
	Code  string `json:"code,omitempty"`
	Param string `json:"param,omitempty"`

	// Sequence number for ordering events
	SequenceNumber int `json:"sequence_number,omitempty"`
}

// ChatCompletionsRequest is the request body for POST /v1/chat/completions.
type ChatCompletionsRequest struct {
	Model               string             `json:"model"`
	Messages            []ChatMessage      `json:"messages"`
	Instructions        string             `json:"instructions,omitempty"` // OpenAI Responses API compat
	MaxTokens           *int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Stream              bool               `json:"stream,omitempty"`
	StreamOptions       *ChatStreamOptions `json:"stream_options,omitempty"`
	Tools               []ChatTool         `json:"tools,omitempty"`
	ParallelToolCalls   *bool              `json:"parallel_tool_calls,omitempty"`
	ToolChoice          json.RawMessage    `json:"tool_choice,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"` // "low" | "medium" | "high" | "xhigh" | "max"
	ServiceTier         string             `json:"service_tier,omitempty"`
	Stop                json.RawMessage    `json:"stop,omitempty"`            // string or []string
	ResponseFormat      json.RawMessage    `json:"response_format,omitempty"` // Chat Completions structured output format

	// Legacy function calling (deprecated but still supported)
	Functions    []ChatFunction  `json:"functions,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
}

// ChatStreamOptions configures streaming behavior.
type ChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatMessage is a single message in the Chat Completions conversation.
type ChatMessage struct {
	Role             string          `json:"role"` // "system" | "user" | "assistant" | "tool" | "function"
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCalls        []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`

	// Legacy function calling
	FunctionCall *ChatFunctionCall `json:"function_call,omitempty"`
}

// ChatContentPart is a typed content part in a multi-modal message.
type ChatContentPart struct {
	Type     string        `json:"type"` // text, image_url, or file
	Text     string        `json:"text,omitempty"`
	ImageURL *ChatImageURL `json:"image_url,omitempty"`
	File     *ChatFile     `json:"file,omitempty"`
}

// ChatImageURL contains the URL for an image content part.
type ChatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto" | "low" | "high"
}

// ChatFile holds a file content part, such as a PDF input.
type ChatFile struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"` // file content encoded as a data URI
	FileID   string `json:"file_id,omitempty"`
}

// ChatTool describes a tool available to the model.
type ChatTool struct {
	Type     string        `json:"type"` // function, web_search, code_execution, or x_search
	Function *ChatFunction `json:"function,omitempty"`

	// type=x_search
	AllowedXHandles          []string `json:"allowed_x_handles,omitempty"`
	ExcludedXHandles         []string `json:"excluded_x_handles,omitempty"`
	FromDate                 string   `json:"from_date,omitempty"`
	ToDate                   string   `json:"to_date,omitempty"`
	EnableImageUnderstanding *bool    `json:"enable_image_understanding,omitempty"`
	EnableVideoUnderstanding *bool    `json:"enable_video_understanding,omitempty"`
}

// ChatFunction describes a function tool definition.
type ChatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ChatToolCall represents a tool call made by the assistant.
// Index is only populated in streaming chunks (omitted in non-streaming responses).
type ChatToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"` // "function"
	Function ChatFunctionCall `json:"function"`
}

// ChatFunctionCall contains the function name and arguments.
type ChatFunctionCall struct {
	// Empty name is omitted so streamed arguments-only deltas never overwrite
	// the tool name a client accumulated from the first delta.
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// ChatCompletionsResponse is the non-streaming response from POST /v1/chat/completions.
type ChatCompletionsResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"` // "chat.completion"
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	Choices           []ChatChoice `json:"choices"`
	Usage             *ChatUsage   `json:"usage,omitempty"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
	ServiceTier       string       `json:"service_tier,omitempty"`
}

// ChatChoice is a single completion choice.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"` // "stop" | "length" | "tool_calls" | "content_filter"
}

// ChatUsage holds token counts in Chat Completions format.
type ChatUsage struct {
	PromptTokens            int               `json:"prompt_tokens"`
	CompletionTokens        int               `json:"completion_tokens"`
	TotalTokens             int               `json:"total_tokens"`
	PromptTokensDetails     *ChatTokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *ChatTokenDetails `json:"completion_tokens_details,omitempty"`
}

// ChatTokenDetails is shared by prompt_tokens_details and completion_tokens_details.
// Zero fields are omitted; callers populate the details applicable to each side.
//
// Common OpenAI CompletionUsage fields are:
//   - prompt_tokens_details: cached_tokens, audio_tokens
//   - completion_tokens_details: reasoning_tokens, audio_tokens,
//     accepted_prediction_tokens, rejected_prediction_tokens
type ChatTokenDetails struct {
	CachedTokens             int `json:"cached_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	CacheCreationTokens      int `json:"cache_creation_tokens,omitempty"`
	CacheWriteTokens         int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// ChatCompletionsChunk is a single streaming chunk from POST /v1/chat/completions.
type ChatCompletionsChunk struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"` // "chat.completion.chunk"
	Created           int64             `json:"created"`
	Model             string            `json:"model"`
	Choices           []ChatChunkChoice `json:"choices"`
	Usage             *ChatUsage        `json:"usage,omitempty"`
	SystemFingerprint string            `json:"system_fingerprint,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
}

// ChatChunkChoice is a single choice in a streaming chunk.
type ChatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        ChatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"` // pointer: null when not final
}

// ChatDelta carries incremental content in a streaming chunk.
type ChatDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          *string        `json:"content,omitempty"` // nil omits the field; a non-nil pointer preserves an empty string
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	Reasoning        *string        `json:"reasoning,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// TextProtocol identifies the upstream protocol for a text request.
type TextProtocol string

// RawOutput returns the retained tool output bytes. Callers must treat the returned slice as read-only.
func (i *ResponsesInputItem) RawOutput() json.RawMessage {
	return i.outputRaw
}

// UnmarshalJSON retains non-string tool output as raw JSON for multimodal protocol conversion.
func (i *ResponsesInputItem) UnmarshalJSON(data []byte) error {
	type alias ResponsesInputItem
	var wire struct {
		*alias
		Output json.RawMessage `json:"output"`
	}

	*i = ResponsesInputItem{}
	wire.alias = (*alias)(i)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	output := bytes.TrimSpace(wire.Output)
	if len(output) == 0 || bytes.Equal(output, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(output, &i.Output); err == nil {
		return nil
	}

	i.outputRaw = append(i.outputRaw[:0], output...)
	i.Output = string(output)
	return nil
}

// UnmarshalJSON accepts a tool name string as shorthand for a custom tool declaration.
func (t *ResponsesTool) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*t = ResponsesTool{Type: "custom", Name: name}
		return nil
	}
	type alias ResponsesTool
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*t = ResponsesTool(a)
	return nil
}

// MarshalJSON encodes tool_search_call using CallID and Arguments.
// Execution is "client" so Codex handles the call; arguments use their raw JSON value.
// Other output types use the default struct encoding.
func (o ResponsesOutput) MarshalJSON() ([]byte, error) {
	type responsesOutputAlias ResponsesOutput
	if o.Type != "tool_search_call" {
		return json.Marshal(responsesOutputAlias(o))
	}
	m := map[string]any{
		"type":      o.Type,
		"id":        o.ID,
		"call_id":   o.CallID,
		"execution": "client",
		"arguments": ToolSearchCallArgumentsJSON(o.Arguments),
	}
	if o.Status != "" {
		m["status"] = o.Status
	}
	return json.Marshal(m)
}

// UnmarshalJSON accepts string function arguments and raw tool_search_call arguments.
// Raw JSON is stored as text because the bridge represents arguments as strings.
func (o *ResponsesOutput) UnmarshalJSON(data []byte) error {
	type responsesOutputAlias ResponsesOutput

	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	if kind.Type != "tool_search_call" {
		var decoded responsesOutputAlias
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		*o = ResponsesOutput(decoded)
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	arguments, hasArguments := fields["arguments"]
	delete(fields, "arguments")
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}

	var decoded responsesOutputAlias
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		return err
	}
	*o = ResponsesOutput(decoded)
	if !hasArguments || string(arguments) == "null" {
		return nil
	}

	var argumentString string
	if err := json.Unmarshal(arguments, &argumentString); err == nil {
		o.Arguments = argumentString
	} else {
		o.Arguments = string(arguments)
	}
	return nil
}

// UnmarshalJSON accepts both Responses and Chat Completions usage field names.
func (u *ResponsesUsage) UnmarshalJSON(data []byte) error {
	type responsesUsageAlias ResponsesUsage
	type cacheTokenPresence struct {
		CacheCreationTokens *int `json:"cache_creation_tokens"`
		CacheWriteTokens    *int `json:"cache_write_tokens"`
	}
	var aux struct {
		responsesUsageAlias
		PromptTokens            int                           `json:"prompt_tokens"`
		CompletionTokens        int                           `json:"completion_tokens"`
		CacheCreationTokens     int                           `json:"cache_creation_tokens"`
		CacheWriteInputTokens   int                           `json:"cache_write_input_tokens"`
		CacheWriteTokens        int                           `json:"cache_write_tokens"`
		PromptTokensDetails     *ResponsesInputTokensDetails  `json:"prompt_tokens_details,omitempty"`
		CompletionTokensDetails *ResponsesOutputTokensDetails `json:"completion_tokens_details,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	var nestedPresence struct {
		InputTokensDetails  *cacheTokenPresence `json:"input_tokens_details"`
		PromptTokensDetails *cacheTokenPresence `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(data, &nestedPresence); err != nil {
		return err
	}
	*u = ResponsesUsage(aux.responsesUsageAlias)
	if u.InputTokens == 0 && aux.PromptTokens != 0 {
		u.InputTokens = aux.PromptTokens
	}
	if u.OutputTokens == 0 && aux.CompletionTokens != 0 {
		u.OutputTokens = aux.CompletionTokens
	}
	if u.CacheCreationInputTokens == 0 {
		switch {
		case aux.CacheWriteInputTokens > 0:
			u.CacheCreationInputTokens = aux.CacheWriteInputTokens
		case aux.CacheCreationTokens > 0:
			u.CacheCreationInputTokens = aux.CacheCreationTokens
		case aux.CacheWriteTokens > 0:
			u.CacheCreationInputTokens = aux.CacheWriteTokens
		}
	}
	if u.InputTokensDetails == nil && aux.PromptTokensDetails != nil {
		u.InputTokensDetails = aux.PromptTokensDetails
	}
	if u.OutputTokensDetails == nil && aux.CompletionTokensDetails != nil {
		u.OutputTokensDetails = aux.CompletionTokensDetails
	}
	var canonicalCacheCreationTokens *int
	switch {
	case nestedPresence.InputTokensDetails != nil && nestedPresence.InputTokensDetails.CacheWriteTokens != nil:
		canonicalCacheCreationTokens = nestedPresence.InputTokensDetails.CacheWriteTokens
	case nestedPresence.PromptTokensDetails != nil && nestedPresence.PromptTokensDetails.CacheWriteTokens != nil:
		canonicalCacheCreationTokens = nestedPresence.PromptTokensDetails.CacheWriteTokens
	case nestedPresence.InputTokensDetails != nil && nestedPresence.InputTokensDetails.CacheCreationTokens != nil:
		canonicalCacheCreationTokens = nestedPresence.InputTokensDetails.CacheCreationTokens
	case nestedPresence.PromptTokensDetails != nil && nestedPresence.PromptTokensDetails.CacheCreationTokens != nil:
		canonicalCacheCreationTokens = nestedPresence.PromptTokensDetails.CacheCreationTokens
	}
	if canonicalCacheCreationTokens != nil {
		u.CacheCreationInputTokens = max(*canonicalCacheCreationTokens, 0)
	}
	if u.TotalTokens == 0 && (u.InputTokens != 0 || u.OutputTokens != 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return nil
}

// UnmarshalJSON accepts string arguments and raw JSON arguments from compatible history replay.
func (c *ChatFunctionCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name
	arguments := bytes.TrimSpace(raw.Arguments)
	if len(arguments) == 0 || string(arguments) == "null" {
		c.Arguments = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(arguments, &text); err == nil {
		c.Arguments = text
		return nil
	}
	c.Arguments = string(arguments)
	return nil
}

// ReasoningText prefers nonempty reasoning_content over the reasoning alias in a message.
func (m ChatMessage) ReasoningText() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// ReasoningText prefers a present reasoning_content field, including an empty string, in a delta.
func (d ChatDelta) ReasoningText() *string {
	if d.ReasoningContent != nil {
		return d.ReasoningContent
	}
	return d.Reasoning
}
