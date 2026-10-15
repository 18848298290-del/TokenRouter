package openaiforward

import (
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// result_projection.go converts forwarding result records between package,
// gateway, and compatibility reader types.
// Forwarding results carry measured usage alongside caller, billing, and vendor
// identities for completion processing. Conversion allocates a destination
// record and copies values, while headers, mutable collections, and pointer
// values remain shared; warning conversion additionally allocates its outer
// warning record.

// ToForwardResult converts a package result into gateway completion data,
// returning nil for nil input. Header maps, slices, and pointer values retain
// their storage; a warning gets a new outer record with its response bytes
// still shared.
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

// FromForwardResult converts gateway completion data into a package result,
// returning nil for nil input. Header maps, slices, and pointer values retain
// their storage; a warning gets a new outer record with its response bytes
// still shared.
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

// FromCompatResult converts compatibility output and attaches the supplied
// billing model, returning nil for nil input. Headers and pointer values remain
// borrowed from the compatibility result, so subsequent mutation requires
// caller-managed copying.
func FromCompatResult(r *openai.CompatResponseResult, billing string) *Result {
	if r == nil {
		return nil
	}
	return &Result{RequestID: r.RequestID, ResponseID: r.ResponseID, Headers: r.UpstreamHeaders, Usage: r.Usage, Model: r.Model, BillingModel: billing, UpstreamModel: r.UpstreamModel, UpstreamResponseServiceTier: r.ServiceTier, ServiceTier: r.ResolvedTier, ReasoningEffort: r.ReasoningEffort, Stream: r.Stream, Duration: r.Duration, FirstTokenMs: r.FirstTokenMs, ClientDisconnect: r.ClientDisconnect, SearchCount: r.SearchCount}
}
