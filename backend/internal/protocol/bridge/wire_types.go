package bridge

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const minMaxOutputTokens = 128

// Aliases share protocol types and JSON codecs with their owning wire packages.
type (
	AnthropicRequest      = anthropic.AnthropicRequest
	AnthropicOutputConfig = anthropic.AnthropicOutputConfig
	AnthropicThinking     = anthropic.AnthropicThinking
	AnthropicMessage      = anthropic.AnthropicMessage
	AnthropicContentBlock = anthropic.AnthropicContentBlock
	AnthropicImageSource  = anthropic.AnthropicImageSource
	AnthropicTool         = anthropic.AnthropicTool
)

type AnthropicResponse = anthropic.AnthropicResponse

type (
	AnthropicUsage               = anthropic.AnthropicUsage
	AnthropicStreamEvent         = anthropic.AnthropicStreamEvent
	AnthropicDelta               = anthropic.AnthropicDelta
	ResponsesRequest             = openai.ResponsesRequest
	ResponsesReasoning           = openai.ResponsesReasoning
	ResponsesText                = openai.ResponsesText
	ResponsesInputItem           = openai.ResponsesInputItem
	ResponsesContentPart         = openai.ResponsesContentPart
	ResponsesTool                = openai.ResponsesTool
	ResponsesResponse            = openai.ResponsesResponse
	ResponsesError               = openai.ResponsesError
	ResponsesIncompleteDetails   = openai.ResponsesIncompleteDetails
	ResponsesOutput              = openai.ResponsesOutput
	WebSearchAction              = openai.WebSearchAction
	ResponsesSummary             = openai.ResponsesSummary
	ResponsesUsage               = openai.ResponsesUsage
	ResponsesInputTokensDetails  = openai.ResponsesInputTokensDetails
	ResponsesOutputTokensDetails = openai.ResponsesOutputTokensDetails
	ResponsesStreamEvent         = openai.ResponsesStreamEvent
	ChatCompletionsRequest       = openai.ChatCompletionsRequest
)

type (
	ChatMessage     = openai.ChatMessage
	ChatContentPart = openai.ChatContentPart
	ChatImageURL    = openai.ChatImageURL
)

type (
	ChatTool                = openai.ChatTool
	ChatFunction            = openai.ChatFunction
	ChatToolCall            = openai.ChatToolCall
	ChatFunctionCall        = openai.ChatFunctionCall
	ChatCompletionsResponse = openai.ChatCompletionsResponse
	ChatChoice              = openai.ChatChoice
	ChatUsage               = openai.ChatUsage
	ChatTokenDetails        = openai.ChatTokenDetails
	ChatCompletionsChunk    = openai.ChatCompletionsChunk
	ChatChunkChoice         = openai.ChatChunkChoice
	ChatDelta               = openai.ChatDelta
)

type ClaudeRequest = anthropic.ClaudeRequest

type ClaudeMessage = anthropic.ClaudeMessage

type ClaudeTool = anthropic.ClaudeTool

type ContentBlock = anthropic.ContentBlock

type ClaudeResponse = anthropic.ClaudeResponse

type ClaudeContentItem = anthropic.ClaudeContentItem

type ClaudeUsage = anthropic.ClaudeUsage

type GeminiContent = gemini.GeminiContent

type GeminiPart = gemini.GeminiPart

type GeminiInlineData = gemini.GeminiInlineData

type GeminiFunctionCall = gemini.GeminiFunctionCall

type GeminiFunctionResponse = gemini.GeminiFunctionResponse

type GeminiGenerationConfig = gemini.GeminiGenerationConfig

type GeminiThinkingConfig = gemini.GeminiThinkingConfig

type GeminiToolDeclaration = gemini.GeminiToolDeclaration

type GeminiCodeExecution = gemini.GeminiCodeExecution

type GeminiFunctionDecl = gemini.GeminiFunctionDecl

type GeminiGoogleSearch = gemini.GeminiGoogleSearch

type GeminiEnhancedContent = gemini.GeminiEnhancedContent

type GeminiImageSearch = gemini.GeminiImageSearch

type GeminiResponse = gemini.GeminiResponse

type GeminiGroundingMetadata = gemini.GeminiGroundingMetadata

type GeminiGroundingChunk = gemini.GeminiGroundingChunk

func AnthropicStopReasonPtr(s string) *string {
	return anthropic.AnthropicStopReasonPtr(s)
}

func AnthropicStopReasonString(p *string) string {
	return anthropic.AnthropicStopReasonString(p)
}

func toolSearchCallArgumentsJSON(arguments string) json.RawMessage {
	return openai.ToolSearchCallArgumentsJSON(arguments)
}
