package bridge

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// wire_types.go exposes protocol-owned wire types to conversation converters
// through aliases.
// Messages, Responses, Chat, and Gemini conversions use the owning protocol
// packages' wire types so custom JSON codecs apply consistently on either side
// of a conversion. Aliases keep those codecs available under the bridge names
// used by request and response adapters.

// minMaxOutputTokens is the output-token floor applied by the Messages and
// Chat request adapters when translating positive token limits.
const minMaxOutputTokens = 128

type (
	// AnthropicRequest aliases Messages request fields consumed by the Responses
	// request adapter.
	AnthropicRequest = anthropic.AnthropicRequest
	// AnthropicOutputConfig aliases Messages output preferences used when
	// selecting Responses reasoning effort.
	AnthropicOutputConfig = anthropic.AnthropicOutputConfig
	// AnthropicThinking aliases Messages thinking settings used to derive
	// converted reasoning controls.
	AnthropicThinking = anthropic.AnthropicThinking
	// AnthropicMessage aliases Messages conversation entries consumed by cross-
	// protocol request conversion.
	AnthropicMessage = anthropic.AnthropicMessage
	// AnthropicContentBlock aliases Messages content variants decoded when
	// translating text and tools.
	AnthropicContentBlock = anthropic.AnthropicContentBlock
	// AnthropicImageSource aliases Messages image references used to construct
	// converted image parts.
	AnthropicImageSource = anthropic.AnthropicImageSource
	// AnthropicTool aliases Messages tool declarations converted into target
	// function tool definitions.
	AnthropicTool = anthropic.AnthropicTool
)

// AnthropicResponse aliases Messages response fields consumed by the Responses
// response adapter.
type AnthropicResponse = anthropic.AnthropicResponse

type (
	// AnthropicUsage aliases Messages token accounting fields carried into
	// converted response usage.
	AnthropicUsage = anthropic.AnthropicUsage
	// AnthropicStreamEvent aliases Messages event fields consumed by the
	// incremental response adapters.
	AnthropicStreamEvent = anthropic.AnthropicStreamEvent
	// AnthropicDelta aliases Messages incremental text, tool, and completion
	// fields read by stream conversion.
	AnthropicDelta = anthropic.AnthropicDelta
	// ResponsesRequest aliases Responses request fields produced and consumed by
	// conversation adapters.
	ResponsesRequest = openai.ResponsesRequest
	// ResponsesReasoning aliases Responses reasoning controls produced from
	// caller-selected thinking settings.
	ResponsesReasoning = openai.ResponsesReasoning
	// ResponsesText aliases Responses output preferences used by the structured-
	// output request adapters.
	ResponsesText = openai.ResponsesText
	// ResponsesInputItem aliases Responses conversation items decoded when
	// translating requests and continuation histories.
	ResponsesInputItem = openai.ResponsesInputItem
	// ResponsesContentPart aliases Responses typed content parts read and written
	// during message conversion.
	ResponsesContentPart = openai.ResponsesContentPart
	// ResponsesTool aliases Responses tool declarations translated into other
	// protocols' tool definitions.
	ResponsesTool = openai.ResponsesTool
	// ResponsesResponse aliases Responses response fields consumed by buffered
	// and streaming conversion.
	ResponsesResponse = openai.ResponsesResponse
	// ResponsesError aliases Responses failure fields passed into converted
	// protocol error events.
	ResponsesError = openai.ResponsesError
	// ResponsesIncompleteDetails aliases Responses incomplete-result reasons
	// mapped into target completion signals.
	ResponsesIncompleteDetails = openai.ResponsesIncompleteDetails
	// ResponsesOutput aliases Responses output items consumed by tool, reasoning,
	// and message conversion.
	ResponsesOutput = openai.ResponsesOutput
	// WebSearchAction aliases Responses hosted-search action fields retained in
	// converted output items.
	WebSearchAction = openai.WebSearchAction
	// ResponsesSummary aliases Responses reasoning summary blocks consumed by
	// plaintext reasoning conversion.
	ResponsesSummary = openai.ResponsesSummary
	// ResponsesUsage aliases Responses token counts and compatibility decoding
	// used by response adapters.
	ResponsesUsage = openai.ResponsesUsage
	// ResponsesInputTokensDetails aliases Responses input-token breakdown carried
	// through cached-input usage conversion.
	ResponsesInputTokensDetails = openai.ResponsesInputTokensDetails
	// ResponsesOutputTokensDetails aliases Responses output-token breakdown
	// carried through reasoning-token usage conversion.
	ResponsesOutputTokensDetails = openai.ResponsesOutputTokensDetails
	// ResponsesStreamEvent aliases Responses stream fields with the owner's
	// event-specific JSON serialization.
	ResponsesStreamEvent = openai.ResponsesStreamEvent
	// ChatCompletionsRequest aliases Chat request fields produced and consumed by
	// conversation adapters.
	ChatCompletionsRequest = openai.ChatCompletionsRequest
)

type (
	// ChatMessage aliases Chat conversation messages read and written during
	// request conversion.
	ChatMessage = openai.ChatMessage
	// ChatContentPart aliases Chat typed message parts used in text and image
	// conversion.
	ChatContentPart = openai.ChatContentPart
	// ChatImageURL aliases Chat image reference fields used when constructing
	// multimodal messages.
	ChatImageURL = openai.ChatImageURL
)

type (
	// ChatTool aliases Chat tool declarations produced from converted client tool
	// definitions.
	ChatTool = openai.ChatTool
	// ChatFunction aliases Chat function definitions used by modern and legacy
	// tool declarations.
	ChatFunction = openai.ChatFunction
	// ChatToolCall aliases Chat assistant tool calls read and written by response
	// conversion.
	ChatToolCall = openai.ChatToolCall
	// ChatFunctionCall aliases Chat function identity and argument fields with
	// compatibility JSON decoding.
	ChatFunctionCall = openai.ChatFunctionCall
	// ChatCompletionsResponse aliases Chat response fields consumed by the
	// buffered response adapters.
	ChatCompletionsResponse = openai.ChatCompletionsResponse
	// ChatChoice aliases Chat completion alternatives carrying messages and their
	// finish reasons.
	ChatChoice = openai.ChatChoice
	// ChatUsage aliases Chat token accounting fields carried into converted
	// response usage.
	ChatUsage = openai.ChatUsage
	// ChatTokenDetails aliases Chat token breakdown fields shared by prompt and
	// completion usage.
	ChatTokenDetails = openai.ChatTokenDetails
	// ChatCompletionsChunk aliases Chat stream chunk fields consumed by
	// incremental response conversion.
	ChatCompletionsChunk = openai.ChatCompletionsChunk
	// ChatChunkChoice aliases Chat stream alternatives carrying deltas and
	// optional finish reasons.
	ChatChunkChoice = openai.ChatChunkChoice
	// ChatDelta aliases Chat incremental content with pointer presence retained
	// by its wire type.
	ChatDelta = openai.ChatDelta
)

// ClaudeRequest aliases Messages wire request fields consumed by Gemini-facing
// compatibility conversion.
type ClaudeRequest = anthropic.ClaudeRequest

// ClaudeMessage aliases Messages wire conversation entries consumed by Gemini
// content conversion.
type ClaudeMessage = anthropic.ClaudeMessage

// ClaudeTool aliases Messages wire tool definitions consumed by Gemini tool
// conversion.
type ClaudeTool = anthropic.ClaudeTool

// ContentBlock aliases Messages wire content variants consumed by Gemini-
// facing compatibility conversion.
type ContentBlock = anthropic.ContentBlock

// ClaudeResponse aliases Messages wire response fields constructed by Gemini
// response conversion.
type ClaudeResponse = anthropic.ClaudeResponse

// ClaudeContentItem aliases Messages wire response content items constructed
// by Gemini response conversion.
type ClaudeContentItem = anthropic.ClaudeContentItem

// ClaudeUsage aliases Messages wire token counts constructed from Gemini usage
// observations.
type ClaudeUsage = anthropic.ClaudeUsage

// GeminiContent aliases Gemini conversation entries used to construct
// GenerateContent request bodies.
type GeminiContent = gemini.GeminiContent

// GeminiPart aliases Gemini text, image, thought, and tool parts used by
// content conversion.
type GeminiPart = gemini.GeminiPart

// GeminiInlineData aliases Gemini inline image bytes and MIME identity used by
// image conversion.
type GeminiInlineData = gemini.GeminiInlineData

// GeminiFunctionCall aliases Gemini function-call fields translated into
// client tool-use content blocks.
type GeminiFunctionCall = gemini.GeminiFunctionCall

// GeminiFunctionResponse aliases Gemini function-result fields constructed
// from client tool-result messages.
type GeminiFunctionResponse = gemini.GeminiFunctionResponse

// GeminiGenerationConfig aliases Gemini generation controls populated by the
// request compatibility adapters.
type GeminiGenerationConfig = gemini.GeminiGenerationConfig

// GeminiThinkingConfig aliases Gemini thinking controls with the owner's
// budget-presence JSON codec.
type GeminiThinkingConfig = gemini.GeminiThinkingConfig

// GeminiToolDeclaration aliases Gemini tool groups used to assemble converted
// function declarations.
type GeminiToolDeclaration = gemini.GeminiToolDeclaration

// GeminiCodeExecution aliases Gemini code-execution tool settings carried by
// converted tool declarations.
type GeminiCodeExecution = gemini.GeminiCodeExecution

// GeminiFunctionDecl aliases Gemini function definitions assembled from
// supported client tool declarations.
type GeminiFunctionDecl = gemini.GeminiFunctionDecl

// GeminiGoogleSearch aliases Gemini Google-search tool settings carried by
// converted tool declarations.
type GeminiGoogleSearch = gemini.GeminiGoogleSearch

// GeminiEnhancedContent aliases Gemini enhanced-content settings carried by
// converted tool declarations.
type GeminiEnhancedContent = gemini.GeminiEnhancedContent

// GeminiImageSearch aliases Gemini image-search settings carried by converted
// tool declarations.
type GeminiImageSearch = gemini.GeminiImageSearch

// GeminiResponse aliases Gemini response fields consumed by compatibility
// response and stream adapters.
type GeminiResponse = gemini.GeminiResponse

// GeminiGroundingMetadata aliases Gemini grounding observations read when
// building converted response content.
type GeminiGroundingMetadata = gemini.GeminiGroundingMetadata

// GeminiGroundingChunk aliases Gemini grounding sources read when constructing
// source text appended to Messages output.
type GeminiGroundingChunk = gemini.GeminiGroundingChunk

// AnthropicStopReasonPtr returns a new pointer containing the supplied stop
// reason. An empty string remains a present value; pass nil separately when
// constructing an unfinished Messages response with a null stop reason.
func AnthropicStopReasonPtr(s string) *string {
	return anthropic.AnthropicStopReasonPtr(s)
}

// AnthropicStopReasonString reads a stop reason and maps nil to an empty
// string. Inspect the pointer separately when the caller needs to distinguish
// an unfinished response from an explicitly empty reason.
func AnthropicStopReasonString(p *string) string {
	return anthropic.AnthropicStopReasonString(p)
}

// toolSearchCallArgumentsJSON delegates argument encoding to the Responses
// wire codec. Blank text becomes an object, valid trimmed JSON retains its
// value, and invalid input becomes a JSON string containing the supplied text.
func toolSearchCallArgumentsJSON(arguments string) json.RawMessage {
	return openai.ToolSearchCallArgumentsJSON(arguments)
}
