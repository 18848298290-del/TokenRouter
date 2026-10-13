package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Gemini creative requests use GenerateContent for generation and
// reference-image edits. Prompt and source controls are adapted to the shared
// image request builder, while the selected upstream model is supplied
// separately to the provider option factory for endpoint and credential
// resolution.

// CreativeGeminiGenerateRequest aliases the shared Gemini image request wire
// representation for creative adapters.
type CreativeGeminiGenerateRequest = geminiwire.ImageGenerateRequest

// ExecuteGemini builds a GenerateContent image request using provider options
// for upstreamModel. Generate and edit are accepted, inpaint and other
// operations fail without retry, and configured Gemini options own credentials
// and transport.
func (e *Target) ExecuteGemini(ctx context.Context, run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) ([]creative.CreativeOutput, error) {
	if run.Operation == creative.CreativeOperationInpaint {
		return nil, creative.CreativeNonRetryableError("gemini platform does not support creative operation %s", run.Operation)
	}
	if run.Operation != creative.CreativeOperationGenerate && run.Operation != creative.CreativeOperationEdit {
		return nil, creative.CreativeNonRetryableError("gemini platform does not support creative operation %s", run.Operation)
	}
	if e.Gemini == nil {
		return nil, errors.New("creative gemini gateway is not configured")
	}
	request := BuildCreativeGeminiRequest(run, payload, upstreamModel)
	options := e.Gemini(upstreamModel)
	return gemininative.GenerateImages(ctx, request, options)
}

// BuildCreativeGeminiRequest delegates prompt, size, ratio, thinking, and
// source image encoding to the shared Gemini builder. upstreamModel is unused
// in this wire body; ExecuteGemini passes it to the option factory for
// endpoint selection.
func BuildCreativeGeminiRequest(run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) CreativeGeminiGenerateRequest {
	input := gemininative.ImageRequestInput{Prompt: payload.Prompt, ImageSize: run.ImageSize, AspectRatio: run.AspectRatio, ThinkingLevel: payload.ThinkingLevel, Sources: make([]upstream.DecodedImage, len(payload.Sources))}
	for i, source := range payload.Sources {
		input.Sources[i] = upstream.DecodedImage{Bytes: source.Bytes, Mime: source.Mime}
	}
	return gemininative.BuildImageRequest(input)
}
