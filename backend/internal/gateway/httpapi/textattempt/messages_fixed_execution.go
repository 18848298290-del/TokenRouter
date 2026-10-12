package textattempt

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
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type Runtime struct{ dependencies *messageExecutionDependencies }

func (r *Runtime) Open(ctx context.Context, in execution.Request, sink upstream.OutputSink) (textflow.MessagePorts, error) {
	output, ok := sink.(*gatewayhttp.MessagesOutput)
	if !ok || output.HTTP == nil {
		return nil, errors.New("messages execution requires its HTTP output adapter")
	}
	// Each bridge supplies single-attempt operations to the shared text loop.
	base := messageAttemptBridge{
		fixed: r.dependencies, c: output.HTTP,
		apiKey: apikey.CopyAPIKey(in.Funding.Key), subject: authctx.AuthSubject{UserID: in.UserID, Concurrency: in.Concurrency},
		subscription: in.Funding.Subscription, parsedReq: in.Text.Parsed, body: in.Body, reqModel: in.Model, reqStream: in.Stream,
		isClaudeCodeClient: in.Metadata.ClaudeCode, platform: in.Text.Platform, hasBoundSession: in.Text.HasBoundSession,
		sessionKey: in.SessionHash, sessionBoundProviderID: in.Text.BoundProviderID, streamStarted: output.StreamStarted, reqLog: output.Log,
	}
	// Messages uses metadata for session isolation and content digests for scheduling.
	if in.Text.Kind == execution.TextMessages && in.Text.Parsed != nil && requeststate.ExecutionHintsFromContext(ctx).SessionIsolationHash == "" {
		if id := gatewayhttp.MetadataSessionID(in.Text.Parsed.MetadataUserID); id != "" {
			ctx = requeststate.WithSessionIsolation(ctx, session.SessionIsolationSourceGateway, id)
		}
	}
	// Use the caller's request context, including state installed during preparation.
	output.HTTP.Request = output.HTTP.Request.WithContext(ctx)
	switch in.Text.Kind {
	case execution.TextGeminiMessages:
		return &geminiMessageAttemptBridge{messageAttemptBridge: base, forwardModel: in.Text.GeminiModel, forwardBody: in.Text.GeminiBody, groupMapping: in.Route.Mapping()}, nil
	case execution.TextGenericResponses:
		return &genericResponsesAttemptBridge{messageAttemptBridge: base, requestCtx: in.Text.SelectionContext, forwardBody: in.AttemptBody, groupMapping: in.Text.Mapping}, nil
	case execution.TextGenericChat:
		return &genericChatAttemptBridge{messageAttemptBridge: base, requestCtx: in.Text.SelectionContext, groupPlatform: in.Text.Platform, selectionSessionHash: in.Text.SelectionSessionHash, groupMapping: in.Text.Mapping}, nil
	case execution.TextNativeGemini:
		return &nativeGeminiAttemptBridge{
			messageAttemptBridge: base,
			modelName:            in.Text.GeminiModel,
			action:               in.Text.Action,
			stream:               in.Stream,
			geminiConcurrency:    output.Concurrency,
			useDigestFallback:    in.Text.UseDigestFallback,
			geminiDigestChain:    in.Text.DigestChain,
			geminiPrefixHash:     in.Text.PrefixHash,
			geminiSessionUUID:    in.Text.SessionUUID,
			matchedDigestChain:   in.Text.MatchedDigestChain,
			groupMapping:         in.Text.Mapping,
			signatureState:       in.Text.SignatureState,
		}, nil
	}
	return &base, nil
}

// messageObservedAttempt converts observed results even when an error accompanies them.
func messageObservedAttempt(result *forwardcore.MessagesResult, err error) upstream.AttemptResult {
	if result == nil {
		return upstream.AttemptResult{Cancelled: errors.Is(err, context.Canceled)}
	}
	out := upstream.AttemptResult{
		RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, Usage: result.Usage,
		Stream: result.Stream, Duration: result.Duration, ClientDisconnect: result.ClientDisconnect,
		UpstreamHeaders: http.Header(result.UpstreamHeaders).Clone(), ImageOutputSizes: slices.Clone(result.ImageOutputSizes),
		ObservedImages: result.ImageCount, SearchCount: result.SearchCount, Cancelled: errors.Is(err, context.Canceled),
	}
	// Metered tokens, images, audio, or searches mark observed usage; RunMessages decides completion eligibility.
	out.HasUsage = result.Usage.HasObservedTokens() || result.ImageCount > 0 || result.AudioUsage != nil || result.SearchCount > 0
	out.Served = err == nil || out.HasUsage
	if result.FirstTokenMs != nil {
		v := *result.FirstTokenMs
		out.FirstTokenMs = &v
	}
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
