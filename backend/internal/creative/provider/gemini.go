package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

type CreativeGeminiGenerateRequest = geminiwire.ImageGenerateRequest

// ExecuteGemini executes with Gemini options configured for Vertex, API key, or OAuth access.
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

func BuildCreativeGeminiRequest(run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) CreativeGeminiGenerateRequest {
	input := gemininative.ImageRequestInput{Prompt: payload.Prompt, ImageSize: run.ImageSize, AspectRatio: run.AspectRatio, ThinkingLevel: payload.ThinkingLevel, Sources: make([]upstream.DecodedImage, len(payload.Sources))}
	for i, source := range payload.Sources {
		input.Sources[i] = upstream.DecodedImage{Bytes: source.Bytes, Mime: source.Mime}
	}
	return gemininative.BuildImageRequest(input)
}
