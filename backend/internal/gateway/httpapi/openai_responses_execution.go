package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIResponsesExecutor binds Responses request construction, execution, and output.
type OpenAIResponsesExecutor struct {
	Requests    *OpenAIRequests
	Output      *OpenAIResponseOutput
	Text        *OpenAITextExecutor
	Grok        *GrokExecutor
	Lineage     *OpenAIEncryptedLineage
	ImageBridge *gatewayprovider.ResponseImagePolicy
}

// Forward prepares, dispatches, and executes one Responses attempt.
func (s *OpenAIResponsesExecutor) Forward(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (out *forwardcore.OpenAIResult, failure error) {
	defer func() { captureResponseModel(c, out) }()
	var routeErr error
	provider, routeErr = gatewayprovider.ProviderForProtocolAttempt(ctx, provider)
	if routeErr != nil {
		return nil, routeErr
	}
	profile := openAIForwardProfile(provider)
	prepared, err := openaiexecution.PreparePrelude(ctx, body, profile, openAIForwardPreludeAdapter{s: s, c: c, provider: provider, ctx: ctx})
	if err != nil {
		return nil, err
	}
	body = prepared.Body
	startTime := prepared.StartedAt
	canonicalImageIntentBody := prepared.CanonicalImageIntentBody
	tlsRouterMatch := prepared.TLS
	originalBody := prepared.OriginalBody
	requestView := prepared.View
	reqModel, reqStream, promptCacheKey := requestView.Model, requestView.Stream, requestView.PromptCacheKey
	originalModel := reqModel
	isCodexCLI := prepared.CodexCLI
	switch prepared.Route {
	case openaiexecution.DispatchRawChat:
		return s.Text.ResponsesViaRawChat(ctx, c, provider, body, tlsRouterMatch)
	case openaiexecution.DispatchGrok:
		return s.Grok.ForwardResponses(ctx, c, provider, body, originalModel, reqStream, startTime)
	case openaiexecution.DispatchAnthropic:
		return s.Text.NativeResponses(ctx, c, provider, body, reqModel)
	case openaiexecution.DispatchPassthrough:
		return s.Text.Passthrough(ctx, c, provider, originalBody, canonicalImageIntentBody, reqModel, prepared.ImageIntentInvalidated, prepared.ReasoningEffort, reqStream, startTime, tlsRouterMatch)
	}

	transformed, err := openaiexecution.TransformRequest(ctx, prepared, profile, openAIForwardTransformAdapter{openAIForwardPreludeAdapter{s: s, c: c, provider: provider, ctx: ctx}})
	if err != nil {
		return nil, err
	}
	body = transformed.Body
	requestView = transformed.View
	reqModel = transformed.Model
	promptCacheKey = transformed.PromptCacheKey
	requestedModel, billingModel, upstreamModel := transformed.RequestedModel, transformed.BillingModel, transformed.UpstreamModel
	imageIntent := transformed.ImageIntent
	fingerprintIDs := transformed.Fingerprint
	reqBody := transformed.Decoded
	// WS and ciphertext recovery reuse the prepared request to avoid repeating model mapping and policy.
	ensureReqBody := func() (map[string]any, error) {
		if requestView.HasPatches() {
			patched, err := requestView.ApplyPatches()
			if err != nil {
				return nil, err
			}
			body = patched
			requestView = requeststate.NewOpenAIRequestView(body)
			reqBody = nil
		}
		if reqBody != nil {
			return reqBody, nil
		}
		decoded, err := requestView.Decode()
		if err != nil {
			return nil, err
		}
		reqBody = decoded
		return reqBody, nil
	}
	// Strip encrypted items already rejected by upstream for this session.
	// This prevents client history from repeatedly triggering rejection, stripping, and retry or reconnect.
	// Derive the lineage session key from the incoming body before retries can rewrite it;
	// a key computed afterward could differ from the next incoming request.
	lineageGroupID := OpenAIResponseGroupID(c)
	lineageEntryBody := body
	lineageSessionHash := ""
	if stateStore := s.Lineage.Store; stateStore != nil && stateStore.HasAnySessionInvalidEncryptedContent() {
		lineageSessionHash = GenerateOpenAISessionHash(c, body)
		if invalidDigests := stateStore.GetSessionInvalidEncryptedContentDigests(lineageGroupID, lineageSessionHash); len(invalidDigests) > 0 {
			strippedBody, strippedCount := s.Lineage.Strip(
				body, invalidDigests, "invalid_encrypted_lineage_strip", provider.Record.ID, 0,
			)
			if strippedCount > 0 {
				body = strippedBody
				requestView = requeststate.NewOpenAIRequestView(body)
				reqBody = nil
			}
		}
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfg media.OpenAIResponsesImageBillingConfig
		var imageCfgErr error
		if reqBody != nil {
			imageCfg, imageCfgErr = gatewayprovider.ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailed(reqBody, billingModel)
		} else {
			imageCfg, imageCfgErr = gatewayprovider.ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, billingModel)
		}
		if imageCfgErr != nil {
			openAIForwardPreludeAdapter{s: s, c: c, provider: provider, ctx: ctx}.Reject(openaiexecution.Rejection{Status: http.StatusBadRequest, Type: "invalid_request_error", Message: imageCfgErr.Error(), Param: "size", ObserveUpstream: true})
			return nil, imageCfgErr
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}

	token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		return nil, err
	}
	SetOpsUpstreamModel(c, upstreamModel)

	reasoningEffort := requeststate.ExtractOpenAIReasoningEffortFromBody(body)
	// Add default effort for Chinese provider models after reqModel has been mapped to billingModel.
	reasoningEffort = gatewayprovider.ApplyThinkingEnabledFallback(reasoningEffort, body, reqModel)
	reasoningEffortValue := ""
	if reasoningEffort != nil {
		reasoningEffortValue = *reasoningEffort
	}
	firstOutputTimeout := time.Duration(0)
	if reqStream && provider.Record.Platform == capability.PlatformOpenAI {
		firstOutputTimeout = s.Output.FirstOutputTimeout(reasoningEffortValue)
	}

	input := openaiexecution.HTTPInput{
		Body: body, LineageEntryBody: lineageEntryBody, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, Platform: provider.Record.Platform,
		RequestedModel: requestedModel, OriginalModel: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel,
		ReasoningEffort: reasoningEffort, ReasoningEffortValue: reasoningEffortValue,
		ImageBillingModel: imageBillingModel, ImageSizeTier: imageSizeTier, ImageInputSize: imageInputSize,
		Stream: reqStream, OAuth: provider.View().IsOAuth(), Shadow: provider.View().IsShadow(), Grok: provider.View().IsGrok(), StartedAt: startTime,
	}
	exchange := openai.HTTPExchangeOptions{
		StartedAt:          startTime,
		FirstOutputTimeout: firstOutputTimeout,
		RequestContext:     gatewayprovider.DetachUpstreamContext,
		Build: func(ctx context.Context, body []byte) (*http.Request, error) {
			return s.Requests.Build(ctx, c, provider, body, token, reqStream, promptCacheKey, isCodexCLI, tlsRouterMatch)
		},
		ApplyHeaders: func(headers http.Header) { openai.ApplyCodexFingerprintHeaders(headers, fingerprintIDs) },
		Do: func(request *http.Request) (*http.Response, error) {
			resetResponseModel(c)
			proxyURL := ""
			if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
				proxyURL = provider.Record.Proxy.URL()
			}
			return s.Requests.Transport.DoWithTLS(request, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, tlsRouterMatch))
		},
		Latency: func(elapsed time.Duration) {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, elapsed.Milliseconds())
		},
		HeaderTimeout: func() error {
			return s.Output.FirstOutputFailure(ctx, c, provider, startTime, originalModel, reasoningEffortValue, firstOutputTimeout, "response_headers", nil)
		},
		TransportError: func(err error) error { return s.Requests.Failure.Handle(ctx, c, provider, err, false) },
	}
	options := s.nativeForwardHTTPOptions(ctx, c, provider, input, exchange,
		func(current []byte) ([]byte, bool, error) {
			return prepareOpenAIHTTPEncryptedRetry(current, func(current []byte) (map[string]any, error) {
				if !bytes.Equal(body, current) {
					body = current
					requestView = requeststate.NewOpenAIRequestView(body)
					reqBody = nil
				}
				return ensureReqBody()
			})
		},
		func(entry []byte) {
			digests := openai.CollectOpenAIEncryptedContentDigestsRaw(entry)
			if len(digests) == 0 {
				return
			}
			if lineageSessionHash == "" {
				lineageSessionHash = GenerateOpenAISessionHash(c, entry)
			}
			s.Lineage.Mark(lineageGroupID, lineageSessionHash, digests)
		},
	)
	options.ReleaseDecodedRequest = func() { reqBody = nil }
	result, err := openaiexecution.RunHTTP(ctx, input, options)
	return openaiexecution.ToForwardResult(result), err
}

func shouldForwardOpenAIResponsesViaRawChatCompletions(provider *gatewayprovider.ExecutionProvider) bool {
	return gatewayprovider.ExecutionModelPolicy(provider).RawChat()
}
