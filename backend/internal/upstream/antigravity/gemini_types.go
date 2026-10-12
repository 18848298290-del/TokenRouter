package antigravity

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// DefaultStopSequences supplies stop markers for converted Claude requests.
var DefaultStopSequences = []string{
	"<|user|>",
	"<|endoftext|>",
	"<|end_of_turn|>",
	"\n\nHuman:",
}

// Shared wire types are aliases; the v1internal envelopes are platform-specific.

// V1InternalRequest wraps a Gemini request with Antigravity routing fields.
type V1InternalRequest struct {
	Project     string        `json:"project"`
	RequestID   string        `json:"requestId"`
	UserAgent   string        `json:"userAgent"`
	RequestType string        `json:"requestType,omitempty"`
	Model       string        `json:"model"`
	Request     GeminiRequest `json:"request"`
}

type GeminiRequest = gemini.GeminiRequest

type GeminiContent = gemini.GeminiContent

type GeminiPart = gemini.GeminiPart

type GeminiGenerationConfig = gemini.GeminiGenerationConfig

type GeminiToolDeclaration = gemini.GeminiToolDeclaration

type GeminiToolConfig = gemini.GeminiToolConfig

type GeminiFunctionCallingConfig = gemini.GeminiFunctionCallingConfig

// V1InternalResponse wraps a Gemini response with Antigravity metadata.
type V1InternalResponse struct {
	Response     GeminiResponse `json:"response"`
	ResponseID   string         `json:"responseId,omitempty"`
	ModelVersion string         `json:"modelVersion,omitempty"`
}

type GeminiResponse = gemini.GeminiResponse
