package antigravity

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// Antigravity v1internal adds project, model, request identity, and request
// type around shared Gemini wire data. Response envelopes carry generation
// data plus optional outer identifiers, while conversion callers select how
// those observations become client model and message identity.

// DefaultStopSequences supplies converted-request stop markers when selected
// model rules permit stop sequences to be emitted with the generation
// configuration. The slice is shared into converted requests, so callers keep
// its contents unchanged while those requests are being serialized.
var DefaultStopSequences = []string{
	"<|user|>",
	"<|endoftext|>",
	"<|end_of_turn|>",
	"\n\nHuman:",
}

// V1InternalRequest carries Antigravity routing metadata around the shared
// Gemini generation request.
type V1InternalRequest struct {
	Project     string        `json:"project"`               // selected account project identifier
	RequestID   string        `json:"requestId"`             // generated agent request identifier
	UserAgent   string        `json:"userAgent"`             // fixed protocol client identity
	RequestType string        `json:"requestType,omitempty"` // agent or search request category
	Model       string        `json:"model"`                 // selected internal model route
	Request     GeminiRequest `json:"request"`               // wrapped Gemini generation data
}

// GeminiRequest represents shared contents and generation controls in Gemini
// wire encoding.
type GeminiRequest = gemini.GeminiRequest

// GeminiContent represents shared ordered content roles and parts in Gemini
// wire encoding.
type GeminiContent = gemini.GeminiContent

// GeminiPart represents shared text, thought, tool, or media data in Gemini
// wire encoding.
type GeminiPart = gemini.GeminiPart

// GeminiGenerationConfig represents shared controls sent within generation
// data in Gemini wire encoding.
type GeminiGenerationConfig = gemini.GeminiGenerationConfig

// GeminiToolDeclaration represents shared function and search tool
// declarations in Gemini wire encoding.
type GeminiToolDeclaration = gemini.GeminiToolDeclaration

// GeminiToolConfig represents shared tool invocation control settings in
// Gemini wire encoding.
type GeminiToolConfig = gemini.GeminiToolConfig

// GeminiFunctionCallingConfig represents shared function-calling mode and
// allowed names in Gemini wire encoding.
type GeminiFunctionCallingConfig = gemini.GeminiFunctionCallingConfig

// V1InternalResponse carries a Gemini response with optional outer response
// and model metadata.
type V1InternalResponse struct {
	Response     GeminiResponse `json:"response"`               // wrapped Gemini response data
	ResponseID   string         `json:"responseId,omitempty"`   // outer response identity observation
	ModelVersion string         `json:"modelVersion,omitempty"` // outer model version observation
}

// GeminiResponse represents shared candidate and usage response observations
// in Gemini wire encoding.
type GeminiResponse = gemini.GeminiResponse
