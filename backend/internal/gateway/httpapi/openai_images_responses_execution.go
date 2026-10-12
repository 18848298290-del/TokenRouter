package httpapi

import (
	"context"
	"errors"
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
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func buildOpenAIImagesResponsesRequest(parsed *gatewaymedia.ImageRequest, toolModel string) ([]byte, error) {
	return openai.BuildOpenAIImagesResponsesRequest(gatewaymedia.NativeImageRequest(parsed), toolModel)
}

func (s *OpenAIImagesExecutor) handleOpenAIImagesErrorResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestBody []byte,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	body := s.Output.ReadErrorBody(resp)

	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	upstreamDetail := ""
	if s.Output.Options.LogUpstreamErrorBody {
		maxBytes := s.Output.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = logredact.TruncateUTF8(string(body), maxBytes)
	}
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	LogOpenAIInstructionsRequiredDebug(ctx, c, provider, resp.StatusCode, upstreamMsg, requestBody, body)

	if s.Output.Options.LogUpstreamErrorBody {
		logging.LegacyPrintf("service.openai_gateway",
			"OpenAI images upstream error %d (provider=%d platform=%s type=%s): %s",
			resp.StatusCode,
			provider.Record.ID,
			provider.Record.Platform,
			provider.Record.Type,
			logredact.TruncateLine(body, s.Output.Options.LogUpstreamErrorBodyMaxBytes),
		)
	}

	var decision providercore.UpstreamErrorDecision
	return nil, gatewaymedia.ResolveImageResponseFailure(resp.StatusCode, upstreamMsg, gatewaymedia.ImageResponseFailurePorts{
		CyberMessage: func() (string, bool) {
			if !gatewayprovider.IsOpenAICyberWarningPayload(body, upstreamMsg) {
				return "", false
			}
			return gatewayprovider.ExtractOpenAICyberWarningMessage(body, upstreamMsg), true
		},
		Observe: func(kind, message string) {
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: kind, Message: message, Detail: upstreamDetail})
		},
		Write: func(response gatewaymedia.ErrorResponse) error {
			upErr := &openai.OpenAIImagesUpstreamError{StatusCode: response.Status, ErrorType: response.Type, Message: response.Message, UpstreamRequestID: strings.TrimSpace(resp.Header.Get("x-request-id"))}
			WriteOpenAIImagesUpstreamErrorResponse(c, upErr)
			return upErr
		},
		WrapCyber: func(cause error) error {
			return gatewayprovider.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, body, gatewayprovider.ExtractOpenAICyberWarningMessage(body, upstreamMsg), cause)
		},
		ApplyPolicy: func() {
			model := ""
			if len(requestedModel) > 0 {
				model = strings.TrimSpace(requestedModel[0])
			}
			decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, resp.StatusCode, resp.Header, body, false, model)
		},
		Generic: func() bool { return decision.ShouldReturnGenericError() },
		Failover: func() bool {
			return decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, body))
		},
		NewFailover: func() error {
			return &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: body, RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode)}
		},
		Rewrite: func() (gatewaymedia.ErrorResponse, bool) {
			status, typ, message, matched := ApplyErrorPassthroughRule(c, provider.Record.Platform, resp.StatusCode, body, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return gatewaymedia.ErrorResponse{Status: status, Type: typ, Message: logredact.SanitizeUpstreamQueries(message)}, matched
		},
		DefaultResponse: func() error {
			upErr := openai.OpenAIImagesUpstreamErrorFromHTTP(resp.StatusCode, resp.Header, body)
			WriteOpenAIImagesUpstreamErrorResponse(c, upErr)
			return upErr
		},
	})
}

func openAIImagesStreamPrefix(parsed *gatewaymedia.ImageRequest) string {
	return openai.OpenAIImagesStreamPrefix(gatewaymedia.NativeImageRequest(parsed))
}

func (s *OpenAIImagesExecutor) forwardOpenAIImagesOAuth(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	parsed *gatewaymedia.ImageRequest,
	groupMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	startTime := time.Now()
	requestModel, upstreamModel, err := gatewaymedia.ResolveImageModels(parsed.Model, groupMappedModel, "gpt-image-2", func(model string) string {
		return gatewayprovider.ExecutionModelPolicy(provider).OpenAIUpstream(model, false)
	})
	if err != nil {
		return nil, err
	}
	if err := s.resolveModelAttributes(ctx, provider, upstreamModel); err != nil {
		return nil, err
	}
	logging.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Images request routing request_model=%s upstream_model=%s endpoint=%s provider_type=%s uploads=%d",
		requestModel,
		upstreamModel,
		parsed.Endpoint,
		provider.Record.Type,
		len(parsed.Uploads),
	)
	upstreamCtx, releaseUpstreamCtx := gatewayprovider.DetachUpstreamContext(ctx)
	defer releaseUpstreamCtx()

	token, _, err := s.Requests.Credentials.Resolve(upstreamCtx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		return nil, err
	}

	responsesBody, err := buildOpenAIImagesResponsesRequest(parsed, upstreamModel)
	if err != nil {
		return nil, err
	}
	upstreamCtx = openai.WithOpenAIImagesSelfBuiltRequest(upstreamCtx)
	upstreamReq, err := s.Requests.Build(upstreamCtx, c, provider, responsesBody, token, true, parsed.StickySessionSeed(), false, tlsRouterMatch...)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("OpenAI-Beta", "responses=experimental")

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	options := s.Output.ImageOptions(c)
	var legacyHTTPResult *forwardcore.OpenAIResult
	httpFailure := false
	retryAgent := false
	target := &mediaprovider.ImagesOptions{
		ProviderID: provider.Record.ID,
		OAuth:      true,
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
			upstreamMsg := ""
			var decision providercore.UpstreamErrorDecision
			retry, err := gatewaymedia.ResolveImageFailure(gatewaymedia.ImageFailurePorts{
				Recover: func() (bool, error) {
					if requeststate.AgentTaskRecoveryTried(ctx) || !s.Requests.Identity.UsesAgentIdentity(ctx, provider) || !openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
						return false, nil
					}
					return true, s.Requests.Identity.Recover(ctx, provider, provider.View().GetCredential("task_id"))
				},
				Failover: func() bool {
					upstreamMsg = logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
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
					decision = s.Output.ApplyHTTPFailure(upstreamCtx, resp, provider, respBody, requestModel)
					return decision.ShouldReturnGenericError()
				},
				NewFailover: func() error {
					retryableOnSameProvider := decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode)
					if provider.View().IsOpenAIOAuthLike() && resp.StatusCode == http.StatusTooManyRequests {
						return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(
							provider,
							resp.StatusCode,
							resp.Header,
							respBody,
							upstreamMsg,
							false,
							retryableOnSameProvider,
						)
					}
					return &forwardcore.UpstreamFailoverError{
						StatusCode: resp.StatusCode,

						ResponseBody: respBody,

						RetryableOnSameProvider: retryableOnSameProvider,
					}
				},
				Handle: func() error {
					var failure error
					legacyHTTPResult, failure = s.handleOpenAIImagesErrorResponse(upstreamCtx, resp, c, provider, responsesBody, requestModel)
					return failure
				},
			})
			retryAgent = retry
			return err
		},

		ResponseError: func(resp *http.Response, before int, err error) error {
			return s.handleOpenAIImagesOAuthResponseError(upstreamCtx, c, provider, requestModel, logredact.SafeUpstreamURL(upstreamReq.URL.String()), resp, before, err)
		},
	}
	protocolID := protocol.ProtocolImagesGenerations
	if parsed.IsEdits() {
		protocolID = protocol.ProtocolImagesEdits
	}
	result, err := (mediaprovider.Images{Options: *target}).Execute(upstreamCtx, upstream.AttemptInput{Protocol: protocolID, ResponseModel: requestModel, Stream: parsed.Stream}, ResponseSink{Writer: c.Writer})
	if retryAgent {
		return s.forwardOpenAIImagesOAuth(requeststate.WithAgentTaskRecovery(ctx), c, provider, parsed, groupMappedModel)
	}
	if httpFailure {
		return legacyHTTPResult, err
	}
	imageCount, retain := gatewaymedia.ImageOutcome(parsed.Stream, true, openai.IsEventStreamResponse(result.UpstreamHeaders), parsed.N, result.ObservedImages, err)
	if !retain {
		return nil, err
	}
	return gatewayprovider.ImagesForwardResult(result, parsed, imageCount), err
}

// shouldCoolOpenAIImagesToolForError reports whether the error came from structured upstream data.
// Callers check the error code before applying cooldown. Text-only model output can be prompt-dependent
// and remains retryable without cooling each provider encountered during failover.
func shouldCoolOpenAIImagesToolForError(upstreamErr *openai.OpenAIImagesUpstreamError) bool {
	return upstreamErr != nil && !upstreamErr.SynthesizedFromModelText
}

func (s *OpenAIImagesExecutor) handleOpenAIImagesOAuthResponseError(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel string,
	upstreamURL string,
	resp *http.Response,
	writerSizeBeforeResponse int,
	err error,
) error {
	responseWritten := c != nil && c.Writer != nil && OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) != writerSizeBeforeResponse
	if code, message, ok := openai.OpenAIUpstreamStreamReadErrorDetails(err); ok {
		// Body transport failures after HTTP success remain retryable until image output starts.
		// Clone headers for failover diagnostics after the response is released.
		headers := http.Header(nil)
		requestID := ""
		statusCode := http.StatusBadGateway
		if resp != nil {
			headers = resp.Header.Clone()
			requestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
		}
		responseBody := []byte(fmt.Sprintf(`{"error":{"type":"upstream_error","code":%q,"message":%q}}`, code, message))
		decision := gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, statusCode, headers, responseBody, false, requestedModel)
		retryable := decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), statusCode, true)
		kind := "http_error"
		if retryable {
			kind = "failover"
			if responseWritten {
				kind = "retry_exhausted_failover"
			}
		}
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform: provider.Record.Platform,

			ProviderID: provider.Record.ID,

			ProviderName: provider.Record.Name,

			UpstreamStatusCode: statusCode,

			UpstreamRequestID: requestID,

			UpstreamURL: upstreamURL,

			Kind: kind,

			Message: message,
		})
		if !retryable || responseWritten {
			return err
		}
		return &forwardcore.UpstreamFailoverError{
			StatusCode: statusCode,

			ResponseBody: responseBody,

			ResponseHeaders: headers,

			RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), statusCode),
		}
	}

	var upstreamErr *openai.OpenAIImagesUpstreamError
	if !errors.As(err, &upstreamErr) {
		return err
	}

	requestID := strings.TrimSpace(upstreamErr.UpstreamRequestID)
	headers := http.Header(nil)
	if resp != nil {
		headers = resp.Header.Clone()
		if requestID == "" {
			requestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
		}
	}
	responseBody := openai.OpenAIImagesUpstreamErrorResponseBody(upstreamErr)
	decision := gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, upstreamErr.StatusCode, headers, responseBody, false, requestedModel)
	retryable := decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), upstreamErr.StatusCode, openai.IsOpenAIImagesRetryableUpstreamError(upstreamErr))
	kind := "http_error"
	if retryable {
		kind = "failover"
		if responseWritten {
			kind = "retry_exhausted_failover"
		}
	}
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform,

		ProviderID: provider.Record.ID,

		ProviderName: provider.Record.Name,

		UpstreamStatusCode: upstreamErr.StatusCode,

		UpstreamRequestID: requestID,

		UpstreamURL: upstreamURL,

		Kind: kind,

		Message: upstreamErr.ClientMessage(),
	})

	if upstreamErr.Code == "image_generation_unavailable" {
		if shouldCoolOpenAIImagesToolForError(upstreamErr) {
			s.Cooldown.Apply(ctx, gatewayprovider.ExecutionRecord(provider))
		}
		if responseWritten {
			return err
		}
		return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(
			provider,
			upstreamErr.StatusCode,
			headers,
			responseBody,
			upstreamErr.ClientMessage(),
			false,
			false,
		)
	}
	if !retryable || responseWritten {
		return err
	}
	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, upstreamErr.StatusCode, headers, responseBody, false, requestedModel).StopScheduling
	return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(
		provider,
		upstreamErr.StatusCode,
		headers,
		responseBody,
		upstreamErr.ClientMessage(),
		shouldDisable,
		!shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(upstreamErr.StatusCode),
	)
}
