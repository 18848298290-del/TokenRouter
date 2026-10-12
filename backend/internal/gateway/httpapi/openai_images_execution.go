package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	mediaprovider "github.com/TokenFlux/TokenRouter/internal/gateway/media/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	openAIImagesGenerationsURL = "https://api.openai.com/v1/images/generations"
	openAIImagesEditsURL       = "https://api.openai.com/v1/images/edits"
)

// OpenAIImagesExecutor executes one image attempt with request building, response output, and tool cooldown
// dependencies.
type OpenAIImagesExecutor struct {
	Requests *OpenAIRequests
	Output   *OpenAIResponseOutput
	Cooldown *provideradapter.ImageToolCooldown
	Enter    func() (func(), error)
}

func (s *OpenAIImagesExecutor) ForwardImages(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	parsed *gatewaymedia.ImageRequest,
	groupMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed images request is required")
	}
	oauth, err := gatewaymedia.ImageExecutionPath(provider.Record.Type)
	if err != nil {
		return nil, err
	}
	if oauth {
		return s.forwardOpenAIImagesOAuth(ctx, c, provider, parsed, groupMappedModel)
	}
	return s.forwardOpenAIImagesAPIKey(ctx, c, provider, body, parsed, groupMappedModel)
}

func (s *OpenAIImagesExecutor) forwardOpenAIImagesAPIKey(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	parsed *gatewaymedia.ImageRequest,
	groupMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	startTime := time.Now()
	requestModel, upstreamModel, err := gatewaymedia.ResolveImageModels(parsed.Model, groupMappedModel, "", func(model string) string {
		return gatewayprovider.ExecutionModelPolicy(provider).OpenAIUpstream(model, false)
	})
	if err != nil {
		return nil, err
	}
	if err := s.resolveModelAttributes(ctx, provider, upstreamModel); err != nil {
		return nil, err
	}
	SetOpsUpstreamModel(c, upstreamModel)
	logging.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Images request routing request_model=%s upstream_model=%s endpoint=%s provider_type=%s",
		strings.TrimSpace(parsed.Model),
		upstreamModel,
		parsed.Endpoint,
		provider.Record.Type,
	)
	forwardBody, forwardContentType, err := upstream.RewriteImageModel(body, parsed.ContentType, upstreamModel)
	if err != nil {
		return nil, err
	}
	// Detach image generation from client cancellation so completion can return billable usage.
	// DetachStreamUpstreamContext propagates cancellation for non-streaming requests, which can discard usage
	// after upstream costs accrue.
	// Upstream response-header waits remain bounded by ResponseHeaderTimeout.
	upstreamCtx, releaseUpstreamCtx := gatewayprovider.DetachUpstreamContext(ctx)
	defer releaseUpstreamCtx()

	token, _, err := s.Requests.Credentials.Resolve(upstreamCtx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		return nil, err
	}
	upstreamReq, err := s.buildOpenAIImagesRequest(upstreamCtx, c, provider, forwardBody, forwardContentType, token, parsed.Endpoint, tlsRouterMatch...)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	options := s.Output.ImageOptions(c)
	options.Backfill = func(body []byte) []byte { return s.backfillOpenAIImagesB64JSON(upstreamCtx, provider, parsed, body) }
	var legacyHTTPResult *forwardcore.OpenAIResult
	httpFailure := false
	target := &mediaprovider.ImagesOptions{
		ProviderID: provider.Record.ID,
		OAuth:      false,
		Model:      upstreamModel,
		StartedAt:  startTime,

		Request: upstreamReq,
		Options: options,
		Enter:   s.Enter,

		ResponseFormat: parsed.ResponseFormat,
		StreamPrefix:   openAIImagesStreamPrefix(parsed),

		Do: func(req *http.Request) (*http.Response, error) {
			upstreamStart := time.Now()
			resp, err := s.Requests.Transport.DoWithTLS(req, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, tlsRouterMatch...))
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
			return resp, err
		},

		TransportError: func(err error) error {
			safeErr := logredact.SanitizeUpstreamQueries(err.Error())
			SetOpsUpstreamError(c, 0, safeErr, "")
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
				Platform:           provider.Record.Platform,
				ProviderID:         provider.Record.ID,
				ProviderName:       provider.Record.Name,
				UpstreamStatusCode: 0,
				UpstreamURL:        logredact.SafeUpstreamURL(upstreamReq.URL.String()),
				Kind:               "request_error",
				Message:            safeErr,
			})
			return fmt.Errorf("upstream request failed: %s", safeErr)
		},

		ReadErrorBody: s.Output.ReadErrorBody,

		RedactErrorBody: func(body []byte) []byte { return s.Requests.Identity.Redact(upstreamCtx, provider, body) },

		HTTPError: func(resp *http.Response, respBody []byte) error {
			httpFailure = true
			upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
			shouldDisable := false
			_, err := gatewaymedia.ResolveImageFailure(gatewaymedia.ImageFailurePorts{
				Failover: func() bool {
					return gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, respBody)
				},
				Observe: func() {
					AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
						Platform: provider.Record.Platform,

						ProviderID: provider.Record.ID,

						ProviderName: provider.Record.Name,

						UpstreamStatusCode: resp.StatusCode,

						UpstreamRequestID: resp.Header.Get("x-request-id"),

						UpstreamURL: logredact.SafeUpstreamURL(upstreamReq.URL.String()),

						Kind: "failover",

						Message: upstreamMsg,
					})
				},
				ApplyPolicy: func() bool {
					shouldDisable = s.Output.ApplyHTTPFailure(upstreamCtx, resp, provider, respBody, upstreamModel).StopScheduling
					return false
				},
				NewFailover: func() error {
					retryableOnSameProvider := !shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode)
					if provider.View().IsOpenAIOAuthLike() && resp.StatusCode == http.StatusTooManyRequests {
						return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(provider, resp.StatusCode, resp.Header, respBody, upstreamMsg, shouldDisable, retryableOnSameProvider)
					}
					if gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(resp.StatusCode, upstreamMsg, respBody) {
						return gatewayprovider.NewOpenAIUpstreamFailure(resp.StatusCode, resp.Header, respBody, upstreamMsg, retryableOnSameProvider)
					}
					return &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, RetryableOnSameProvider: retryableOnSameProvider}
				},
				Handle: func() error {
					var failure error
					legacyHTTPResult, failure = s.handleOpenAIImagesErrorResponse(upstreamCtx, resp, c, provider, forwardBody, upstreamModel)
					return failure
				},
			})
			return err
		},
	}
	protocolID := protocol.ProtocolImagesGenerations
	if parsed.IsEdits() {
		protocolID = protocol.ProtocolImagesEdits
	}
	result, err := (mediaprovider.Images{Options: *target}).Execute(upstreamCtx, upstream.AttemptInput{Protocol: protocolID, ResponseModel: requestModel, Stream: parsed.Stream}, ResponseSink{Writer: c.Writer})
	if httpFailure {
		return legacyHTTPResult, err
	}
	imageCount, retain := gatewaymedia.ImageOutcome(parsed.Stream, false, upstreamopenai.IsEventStreamResponse(result.UpstreamHeaders), parsed.N, result.ObservedImages, err)
	if !retain {
		return nil, err
	}
	return gatewayprovider.ImagesForwardResult(result, parsed, imageCount), err
}

func (s *OpenAIImagesExecutor) buildOpenAIImagesRequest(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	contentType string,
	token string,
	endpoint string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*http.Request, error) {
	targetURL, err := s.Requests.ImagesURL(provider, endpoint)
	if err != nil {
		return nil, err
	}

	options := s.Requests.ResponseOptions(ctx, c, provider, token, targetURL, false, tlsRouterMatch...)
	options.AllowHeader = AllowOpenAIPassthroughHeader
	return upstreamopenai.BuildImagesRequest(ctx, body, contentType, options)
}
