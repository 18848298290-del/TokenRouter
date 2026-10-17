package openaiattempt

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// openai_fixed_execution.go opens protocol-specific attempt adapters and
// copies execution observations.
// Each HTTP text request opens fresh provider-attempt state while runtime
// services remain shared with app assembly. Responses, Chat, and Messages
// select different adapters from the request kind, using the same admitted key,
// output adapter, routing context, and request activity rules.
//
// Observed results retain protocol usage separately from HTTP commitment and
// retry decisions. Native usage replaces generic token buckets when available,
// while image, search, and audio observations also establish usage presence.
// Headers, image sizes, and optional scalar pointers are copied into attempt
// observations before the text loop makes its completion decision. Borrowed
// request bodies and service references remain stable for the serial request
// lifetime.

// Runtime retains shared attempt dependencies and opens request-local protocol
// adapters.
type Runtime struct {
	// dependencies retains the app's shared services and callbacks. Open
	// creates fresh request state while these references remain available
	// for attempt and completion work.
	dependencies *openAIExecutionDependencies
}

// Open binds one admitted text request to its HTTP output and protocol attempt
// adapter. Callers provide a nonnil MessagesOutput with a request-bearing HTTP
// context; other sink types return an adapter error. The effective key is
// copied, while request bodies, subscription, and shared services remain
// borrowed.
//
// The output request context is replaced by ctx before adapter selection. Chat
// and Messages retain their additional prompt/mapping state; other kinds use
// the Responses bridge. Messages constructs a lazy mapped-body cache so its
// first actual mapping occurs during forwarding rather than runtime opening.
func (r *Runtime) Open(ctx context.Context, in execution.Request, sink upstream.OutputSink) (textflow.ResponsePorts, error) {
	output, ok := sink.(*gatewayhttp.MessagesOutput)
	if !ok || output.HTTP == nil {
		return nil, errors.New("openai text execution requires its HTTP output adapter")
	}
	output.HTTP.Request = output.HTTP.Request.WithContext(ctx)
	base := responsesAttemptBridge{
		fixed: r.dependencies, c: output.HTTP, apiKey: apikey.CopyAPIKey(in.Funding.Key), subject: authctx.AuthSubject{UserID: in.UserID, Concurrency: in.Concurrency}, subscription: in.Funding.Subscription, reqLog: output.Log,
		body: in.Body, forwardBody: in.AttemptBody, sessionHashBody: in.Text.SessionHashBody, reqModel: in.Model, forwardModel: in.Text.ForwardModel, sessionHash: in.SessionHash, previousResponseID: in.Text.PreviousResponseID, requestPlatform: in.Text.Platform, reqStream: in.Stream,
		nativeCompactionV2: in.Text.NativeCompactionV2, legacyCompact: in.Text.LegacyCompact, requireCompact: in.Text.RequireCompact, streamStarted: output.StreamStarted, selectionCtx: in.Text.SelectionContext, groupMapping: in.Text.Mapping, routingStart: in.Text.RoutingStart, requiredCapability: in.Text.RequiredCapability,
	}
	switch in.Text.Kind {
	case execution.TextOpenAIChat:
		return &openAIChatAttemptBridge{responsesAttemptBridge: base, promptCacheKey: in.Text.PromptCacheKey}, nil
	case execution.TextOpenAIMessages:
		// Defer model-body replacement until forwarding reads this
		// request-local cache, keeping attempt opening independent of
		// actual mapped-byte construction and its diagnostics.
		mapped := requeststate.NewModelMappedBodyCache(in.Body, r.dependencies.replaceModelInBody)
		return &openAIMessageAttemptBridge{
			responsesAttemptBridge: base,
			providerLayerModel:     in.Text.ProviderLayerModel,
			currentRoutingModel:    in.Text.ProviderLayerModel,
			promptCacheKey:         in.Text.PromptCacheKey,
			groupMappingMsg:        in.Text.Mapping,
			mappedBodyForMessages:  mapped,
		}, nil
	default:
		return &base, nil
	}
}

// openAIObservedAttempt captures usage and result observations for the serial
// text loop. Nil results return cancellation evidence alone. With a result,
// generic usage is populated first and NativeUsage takes precedence; image,
// search, audio, and image-input observations also contribute to HasUsage.
//
// Served is true after error-free execution or any observed usage, allowing the
// loop to retain billable partial work. Header maps, image-size slices,
// performance maps, and the audio struct are copied at their respective
// levels. Service tier and reasoning effort are passed by value, and
// cancellation is classified from the error chain independently of usage
// presence.
func openAIObservedAttempt(result *forwardcore.OpenAIResult, err error) upstream.AttemptResult {
	if result == nil {
		return upstream.AttemptResult{Cancelled: errors.Is(err, context.Canceled)}
	}
	out := upstream.AttemptResult{
		RequestAttempt:   result.RequestAttempt,
		RequestID:        result.RequestID,
		ResponseID:       result.ResponseID,
		Model:            result.Model,
		UpstreamModel:    result.UpstreamModel,
		Stream:           result.Stream,
		Duration:         result.Duration,
		ClientDisconnect: result.ClientDisconnect,
		UpstreamHeaders:  http.Header(result.UpstreamHeaders).Clone(),
		ObservedImages:   result.ImageCount,
		ImageOutputSizes: slices.Clone(result.ImageOutputSizes),
		SearchCount:      result.SearchCount,
		Cancelled: errors.Is(err,
			context.Canceled),
	}
	out.Usage = upstream.TokenUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, CacheCreationInputTokens: result.Usage.CacheCreationInputTokens, CacheReadInputTokens: result.Usage.CacheReadInputTokens, ImageOutputTokens: result.Usage.ImageOutputTokens}
	// Native protocol usage supplies its own cache buckets and speed.
	// Prefer that complete value over generic OpenAI buckets before
	// deriving the loop's usage-presence observation.
	if result.NativeUsage != nil {
		out.Usage = *result.NativeUsage
	}
	out.ImageInputTokens = result.Usage.ImageInputTokens
	out.HasUsage = out.Usage.HasObservedTokens() || result.ImageCount > 0 || result.SearchCount > 0 || result.WebSearchCalls > 0 || result.AudioUsage != nil || result.Usage.ImageInputTokens > 0
	// Usage-bearing partial results can remain billable after a forwarding
	// error. Keep that served observation separate from error-free
	// completion and client HTTP commitment.
	out.Served = err == nil || out.HasUsage
	out.Timings = result.Timings.Clone()
	if result.ServiceTier != nil {
		out.ServiceTier = *result.ServiceTier
	}
	if result.ReasoningEffort != nil {
		out.ReasoningEffort = *result.ReasoningEffort
	}
	if result.AudioUsage != nil {
		v := *result.AudioUsage
		out.AudioUsage = &v
	}
	return out
}
