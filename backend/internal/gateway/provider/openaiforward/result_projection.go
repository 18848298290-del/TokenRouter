package openaiforward

import (
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func ToForwardResult(result *Result) *forwardcore.OpenAIResult {
	if result == nil {
		return nil
	}
	value := &forwardcore.OpenAIResult{
		UpstreamEndpoint:            result.UpstreamEndpoint,
		RequestedReasoningEffort:    result.RequestedReasoningEffort,
		OpenAIWSMode:                result.OpenAIWSMode,
		UpstreamTerminalEvent:       result.UpstreamTerminalEvent,
		ResponseHeaders:             result.ResponseHeaders,
		ImageOutputSize:             result.ImageOutputSize,
		ImageSizeSource:             result.ImageSizeSource,
		ImageSizeBreakdown:          result.ImageSizeBreakdown,
		VideoCount:                  result.VideoCount,
		VideoResolution:             result.VideoResolution,
		VideoDurationSeconds:        result.VideoDurationSeconds,
		WebSearchCalls:              result.WebSearchCalls,
		AudioUsage:                  result.AudioUsage,
		ClientDisconnect:            result.ClientDisconnect,
		SearchCount:                 result.SearchCount,
		RequestID:                   result.RequestID,
		ResponseID:                  result.ResponseID,
		UpstreamHeaders:             result.Headers,
		Usage:                       result.Usage,
		Model:                       result.Model,
		BillingModel:                result.BillingModel,
		UpstreamModel:               result.UpstreamModel,
		UpstreamResponseServiceTier: result.UpstreamResponseServiceTier,
		UpstreamResponseModel:       result.UpstreamResponseModel,
		ServiceTier:                 result.ServiceTier,
		ReasoningEffort:             result.ReasoningEffort,
		Stream:                      result.Stream,
		Duration:                    result.Duration,
		FirstTokenMs:                result.FirstTokenMs,
		ImageCount:                  result.ImageCount,
		ImageSize:                   result.ImageSize,
		ImageInputSize:              result.ImageInputSize,
		ImageOutputSizes:            result.ImageOutputSizes,
	}
	if result.UpstreamWarning != nil {
		value.UpstreamWarning = &forwardcore.UpstreamWarning{StatusCode: result.UpstreamWarning.StatusCode, ResponseBody: result.UpstreamWarning.ResponseBody, Message: result.UpstreamWarning.Message}
	}
	return value
}

// FromForwardResult converts a gateway completion result into this package's forwarding result.
func FromForwardResult(r *forwardcore.OpenAIResult) *Result {
	if r == nil {
		return nil
	}
	value := &Result{
		UpstreamEndpoint:            r.UpstreamEndpoint,
		RequestedReasoningEffort:    r.RequestedReasoningEffort,
		OpenAIWSMode:                r.OpenAIWSMode,
		UpstreamTerminalEvent:       r.UpstreamTerminalEvent,
		ResponseHeaders:             r.ResponseHeaders,
		ImageOutputSize:             r.ImageOutputSize,
		ImageSizeSource:             r.ImageSizeSource,
		ImageSizeBreakdown:          r.ImageSizeBreakdown,
		VideoCount:                  r.VideoCount,
		VideoResolution:             r.VideoResolution,
		VideoDurationSeconds:        r.VideoDurationSeconds,
		WebSearchCalls:              r.WebSearchCalls,
		AudioUsage:                  r.AudioUsage,
		RequestID:                   r.RequestID,
		ResponseID:                  r.ResponseID,
		Headers:                     r.UpstreamHeaders,
		Usage:                       r.Usage,
		Model:                       r.Model,
		BillingModel:                r.BillingModel,
		UpstreamModel:               r.UpstreamModel,
		UpstreamResponseServiceTier: r.UpstreamResponseServiceTier,
		UpstreamResponseModel:       r.UpstreamResponseModel,
		ServiceTier:                 r.ServiceTier,
		ReasoningEffort:             r.ReasoningEffort,
		Stream:                      r.Stream,
		Duration:                    r.Duration,
		FirstTokenMs:                r.FirstTokenMs,
		ClientDisconnect:            r.ClientDisconnect,
		SearchCount:                 r.SearchCount,
		ImageCount:                  r.ImageCount,
		ImageSize:                   r.ImageSize,
		ImageInputSize:              r.ImageInputSize,
		ImageOutputSizes:            r.ImageOutputSizes,
	}
	if r.UpstreamWarning != nil {
		value.UpstreamWarning = &forwardcore.UpstreamWarning{StatusCode: r.UpstreamWarning.StatusCode, ResponseBody: r.UpstreamWarning.ResponseBody, Message: r.UpstreamWarning.Message}
	}
	return value
}

// FromCompatResult converts a compatibility response result and attaches the billing model.
func FromCompatResult(r *openai.CompatResponseResult, billing string) *Result {
	if r == nil {
		return nil
	}
	return &Result{RequestID: r.RequestID, ResponseID: r.ResponseID, Headers: r.UpstreamHeaders, Usage: r.Usage, Model: r.Model, BillingModel: billing, UpstreamModel: r.UpstreamModel, UpstreamResponseServiceTier: r.ServiceTier, ServiceTier: r.ResolvedTier, ReasoningEffort: r.ReasoningEffort, Stream: r.Stream, Duration: r.Duration, FirstTokenMs: r.FirstTokenMs, ClientDisconnect: r.ClientDisconnect, SearchCount: r.SearchCount}
}
