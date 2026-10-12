package grokforward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Forward prepares a Grok request and processes output, using ResponsesExecutor for transport recovery.
func Forward(ctx context.Context, p Ports, o Options, in Input) (*forwardcore.OpenAIResult, error) {
	body, originalModel, reqStream, startTime := in.Body, in.OriginalModel, in.Stream, in.StartedAt

	if in.ProviderType != "oauth" && in.ProviderType != "apikey" {
		return nil, fmt.Errorf("grok provider type %s is not supported by Responses forwarding", in.ProviderType)
	}
	billingModel := p.BillingModel(originalModel)
	upstreamModel := p.UpstreamModel(billingModel)
	if o.ResolveAttributes != nil {
		attributes, err := o.ResolveAttributes(ctx, upstreamModel)
		if err != nil {
			return nil, err
		}
		o.Codec.Attributes = func(model string) modelcatalog.Attributes {
			if model == upstreamModel {
				return attributes
			}
			return modelcatalog.Attributes{}
		}
	}
	if p.ImageModel(upstreamModel) {
		err := fmt.Errorf("model %s is an image model and is not available on the Responses endpoint; use /v1/images/generations instead", upstreamModel)
		// Report the endpoint mismatch as 400 before the handler can convert it to a generic 502.
		if in.HTTPPresent {
			p.InvalidRequest(err.Error(), "model")
		}
		return nil, err
	}
	patchedBody, clientToolMapping, err := o.Codec.PatchGrokResponsesBodyWithClientTools(body, upstreamModel)
	if err != nil {
		p.SetError(http.StatusBadRequest, err.Error(), "")
		p.InvalidRequest(err.Error(), "tools")
		return nil, err
	}
	p.ClientTools(clientToolMapping)
	// Emulate Compact with a Responses summary turn and wrap the result as a compaction item.
	if in.Compact {
		patchedBody, err = o.Codec.BuildGrokCompactRequestBody(patchedBody)
		if err != nil {
			return nil, err
		}
	}
	// Derive cache identity from the upstream payload so additional_tools contributes
	// to the stable tool prefix. Use the original body when Claude Code session identity
	// would otherwise be lost during metadata stripping.
	cacheIdentityBody := patchedBody
	if grok.ExtractClaudeCodeSessionIDFromPayload(body) != "" {
		cacheIdentityBody = body
	}
	cacheIdentity := p.CacheIdentity(cacheIdentityBody, upstreamModel)
	mixedCacheIntentBody := append([]byte(nil), patchedBody...)
	patchedBody, err = grok.ApplyGrokResponsesCacheIdentity(patchedBody, body, cacheIdentity, in.OAuth)
	if err != nil {
		return nil, fmt.Errorf("apply grok prompt cache identity: %w", err)
	}
	// Add web_search and x_search to the Free OAuth client-tool route
	// to avoid routing it to the uncacheable build-free tier.
	patchedBody, err = p.FreeCacheRoute(patchedBody, mixedCacheIntentBody, cacheIdentity)
	if err != nil {
		return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", err)
	}
	err = p.Credential(ctx)
	if err != nil {
		return nil, err
	}
	upstreamCtx, releaseUpstreamCtx := p.Detach(ctx)
	defer releaseUpstreamCtx()
	p.ResolveProxy()
	upstreamStart := time.Now()
	var handled bool
	var handledResult *forwardcore.OpenAIResult
	var handleErr error
	target := &grok.ResponsesTarget{
		ProviderID: in.ProviderID,
		Model:      upstreamModel,
		Enter:      o.Enter,
		Exchange: grok.ResponsesExchange{
			Build: func(body []byte) (*http.Request, error) {
				return p.Build(upstreamCtx, body, cacheIdentity, true)
			},
			Do: func(req *http.Request) (*http.Response, error) {
				return p.Do(req)
			},
			ReadError: p.ReadError,
			AfterExchange: func(err error) error {
				p.Latency(time.Since(upstreamStart).Milliseconds())
				if err != nil {
					return p.TransportError(ctx, err)
				}
				return nil
			},
			OnReplay: func() {
				p.ReplayNotice(cacheIdentity != "")
			},
		},
		BeforeResponse: func(resp *http.Response, body []byte) (bool, error) {
			patchedBody = body
			if resp.StatusCode >= 400 {
				handled = true
				respBody := p.ReadError(resp)
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				upstreamMsg := p.ErrorMessage(respBody)
				if upstreamMsg == "" {
					upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
				}
				decision := p.Health(ctx, resp.StatusCode, resp.Header, respBody, upstreamModel, true)
				kind := "http_error"
				if decision.Failover {
					kind = "failover"
				}
				p.Observe(Notice{
					Platform:           in.Platform,
					ProviderID:         in.ProviderID,
					ProviderName:       in.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
					Kind:               kind,
					Message:            upstreamMsg,
				})
				if decision.Generic {
					handledResult, handleErr = p.HandleError(ctx, resp, patchedBody, upstreamModel)
					return true, handleErr
				}
				// Quota and rate limits affect the team-model overlay; capacity pressure does not hide healthy providers.
				if p.ShouldMarkTeam(resp.StatusCode, respBody) {
					p.MarkTeam(upstreamModel)
				}
				if kind == "failover" {
					retry := p.RetryMetadata(resp.StatusCode, respBody)
					return true, p.Failure(Failure{
						StatusCode:                resp.StatusCode,
						ResponseBody:              respBody,
						ResponseHeaders:           resp.Header.Clone(),
						RetryableOnSameProvider:   retry.Retryable || decision.RetrySameProvider,
						RequestScopedTransient:    retry.Retryable && resp.StatusCode == http.StatusTooManyRequests,
						SameProviderRetryDelay:    retry.Delay,
						SameProviderRetryDeadline: retry.Deadline,
						SameProviderRetryMax:      retry.Max,
					})
				}
				handledResult, handleErr = p.HandleError(ctx, resp, patchedBody, upstreamModel)
				return true, handleErr
			}
			p.ObserveSuccess(ctx, resp.Header, resp.StatusCode, upstreamModel)
			return false, nil
		},
		MaxLineSize: o.MaxLineSize,
		ClientTools: clientToolMapping,
		ReadResponse: func(resp *http.Response, input upstream.AttemptInput, _ upstream.OutputSink) (upstream.ResponsesObservation, error) {
			if input.Stream {
				return p.ReadStream(ctx, resp, startTime, originalModel, upstreamModel)
			}
			return p.ReadNonStream(ctx, resp, originalModel, upstreamModel)
		},
	}
	sink := p.Sink()
	nativeResult, err := (grok.ResponsesExecutor{}).Execute(upstreamCtx, upstream.AttemptInput{
		Protocol:      protocol.ProtocolOpenAIResponses,
		Body:          patchedBody,
		ResponseModel: originalModel,
		Stream:        reqStream,
		Target:        target,
	}, sink)
	if handled {
		return handledResult, err
	}
	if err != nil {
		return nil, err
	}
	usage := &protocolopenai.ForwardUsage{
		InputTokens:              nativeResult.Usage.InputTokens,
		ImageInputTokens:         nativeResult.ImageInputTokens,
		OutputTokens:             nativeResult.Usage.OutputTokens,
		CacheCreationInputTokens: nativeResult.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     nativeResult.Usage.CacheReadInputTokens,
		ImageOutputTokens:        nativeResult.Usage.ImageOutputTokens,
	}
	firstTokenMs := nativeResult.FirstTokenMs
	responseID := nativeResult.ResponseID
	searchCount := nativeResult.SearchCount
	imageCount := nativeResult.ObservedImages
	imageOutputSizes := nativeResult.ImageOutputSizes
	reasoningEffort := p.Effort(patchedBody, originalModel)
	result := &forwardcore.OpenAIResult{
		RequestID:             nativeResult.RequestID,
		UpstreamResponseModel: nativeResult.UpstreamResponseModel,
		UpstreamHeaders:       nativeResult.UpstreamHeaders,
		ResponseID:            responseID,
		Usage:                 *usage,
		Model:                 originalModel,
		BillingModel:          billingModel,
		UpstreamModel:         upstreamModel,
		ReasoningEffort:       reasoningEffort,
		Stream:                reqStream,
		OpenAIWSMode:          false,
		ResponseHeaders:       nativeResult.UpstreamHeaders.Clone(),
		Duration:              time.Since(startTime),
		FirstTokenMs:          firstTokenMs,
	}
	// Carry search and image counts into the result so settlement can apply
	// per-search and per-image prices.
	if searchCount > 0 {
		result.SearchCount = searchCount
	}
	if imageCount > 0 {
		result.ImageCount = imageCount
		result.ImageOutputSizes = imageOutputSizes
	}
	return result, nil
}
