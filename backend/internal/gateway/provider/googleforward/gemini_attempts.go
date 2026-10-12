package googleforward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

const (
	geminiMaxRetries = 5

	geminiAppliedTempPolicyHeader = "X-TokenRouter-Internal-Temp-Policy-Applied"

	// Fill missing functionCall thought signatures with the compatibility sentinel.
	// Protocol reference: https://ai.google.dev/gemini-api/docs/thought-signatures
	geminiDummyThoughtSignature = "skip_thought_signature_validator"

	geminiCustomCodeSkippedClientMessage = "Upstream gateway error"
)

func (s *Gemini) readUpstreamErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	limit := int64(512 << 10)
	if s != nil && s.Options.Configured && s.Options.LogErrorBody && s.Options.LogErrorBodyMaxBytes > int(limit) {
		limit = int64(s.Options.LogErrorBodyMaxBytes)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return body
}

func (s *Gemini) validateUpstreamBaseURL(raw string) (string, error) {
	if s.Options.Configured && !s.Options.URLAllowlistEnabled {
		normalized, err := egress.ValidateURLFormat(raw, s.Options.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid base_url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := egress.ValidateHTTPSURL(raw, egress.ValidationOptions{
		AllowedHosts: s.Options.AllowedHosts,

		RequireAllowlist: true,

		AllowPrivate: s.Options.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

// Forward prepares one Messages-to-Gemini attempt; the outer gateway selects replacement providers.
// @project-doc docs/interfaces/gemini_upstream.md#gemini_native_execution
func (s *Gemini) Forward(ctx context.Context, output Output, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
	c := &attempt{Output: output}

	c.Images = 0
	startTime := time.Now()

	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("missing model")
	}

	originalModel := req.Model
	// Apply model mappings for every credential type so OAuth execution agrees with selection and catalog resolution.
	mappedModel := providercore.ResolveForwardMappedModel(gatewayprovider.ExecutionRecord(provider), req.Model, provideradapter.ModelDefaults())

	geminiReq, err := convertClaudeMessagesToGeminiGenerateContent(body)
	if err != nil {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	attributes, err := gatewayprovider.ResolveModelAttributes(ctx, provider, mappedModel, s.Attributes, "gemini_generate_content")
	if err != nil {
		return nil, err
	}
	geminiReq, err = applyGeminiModelParameters(geminiReq, body, attributes, true)
	if err != nil {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	geminiReq = ensureGeminiFunctionCallThoughtSignatures(geminiReq)
	originalClaudeBody := body

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	requestIDHeader := "x-request-id"
	switch provider.Record.Type {
	case capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth, capability.ProviderTypeServiceAccount:
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", provider.Record.Type)
	}
	useUpstreamStream := req.Stream
	if provider.Record.Type == capability.ProviderTypeOAuth && !req.Stream && strings.TrimSpace(provider.View().GetCredential("project_id")) != "" {
		useUpstreamStream = true
	}
	plan := s.geminiRequestPlan(provider, mappedModel, "", false, req.Stream, useUpstreamStream, false)
	buildReq := func(ctx context.Context) (*http.Request, string, error) {
		return gemininative.BuildRequest(ctx, geminiReq, plan)
	}

	options := s.geminiExchangeOptions(c, ctx, provider, mappedModel, geminiExchangeMessages, gemininative.OpenAICompatChatCompletions)
	options.Build = buildReq
	options.RequestIDHeader = requestIDHeader
	options.Do = func(req *http.Request) (*http.Response, error) {
		return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
	}
	options.FilterThinking = func() []byte { return gatewayprovider.FilterThinkingBlocksForRetry(originalClaudeBody, originalModel) }
	options.FilterTools = func() []byte {
		return gatewayprovider.FilterSignatureSensitiveBlocksForRetry(originalClaudeBody, originalModel)
	}
	options.ConvertRetry = func(stripped []byte) ([]byte, error) {
		converted, err := convertClaudeMessagesToGeminiGenerateContent(stripped)
		if err != nil {
			return nil, err
		}
		converted, err = applyGeminiModelParameters(converted, stripped, attributes, false)
		if err == nil && attributes.Attributes.HasReasoningOption("toggle") {
			converted, err = sjson.SetBytes(converted, "generationConfig.thinkingConfig", bridge.GeminiThinkingConfig{BudgetSet: true})
		}
		return converted, err
	}
	options.ReplaceBody = func(value []byte) { geminiReq = value }
	var requestID string
	var compatibilityResult *forwardcore.MessagesResult
	stopped := false
	target := &gemininative.Target{
		ProviderID:     provider.Record.ID,
		Model:          mappedModel,
		Mode:           gemininative.MessagesResponse,
		Exchange:       options,
		Response:       s.geminiResponseAdapter(c).Options,
		StartedAt:      startTime,
		UpstreamStream: useUpstreamStream,
		OAuth:          provider.Record.Type == capability.ProviderTypeOAuth,
		Enter:          s.Enter,
	}
	target.BeforeResponse = func(ctx context.Context, resp *http.Response, requestIDHeader string) (bool, error) {
		var callbackErr error
		compatibilityResult, callbackErr = func() (*forwardcore.MessagesResult, error) {
			if resp.StatusCode >= 400 {
				respBody := s.readUpstreamErrorBody(resp)
				decision := s.applyGeminiUpstreamErrorPolicy(ctx, provider, resp.StatusCode, resp.Header, respBody, mappedModel)
				upstreamReqID := resp.Header.Get(requestIDHeader)
				if upstreamReqID == "" {
					upstreamReqID = resp.Header.Get("x-goog-request-id")
				}
				if decision.Policy == providercore.ErrorPolicyCustomSkipped || decision.Policy == providercore.ErrorPolicyPoolBypassed {
					if failoverErr := s.skippedErrorPolicyFailoverError(c, provider, resp.StatusCode, respBody, upstreamReqID); failoverErr != nil {
						return nil, failoverErr
					}
					if decision.Policy == providercore.ErrorPolicyCustomSkipped {
						return nil, c.GeminiCustomCodeSkippedError(provider, resp.StatusCode, upstreamReqID, respBody, func() {
							_ = c.ClaudeError(http.StatusInternalServerError, "api_error", geminiCustomCodeSkippedClientMessage)
						})
					}
					return nil, c.GeminiMappedError(provider, resp.StatusCode, upstreamReqID, respBody)
				}
				if decision.ShouldReturnGenericError() {
					genericBody := []byte(`{"error":{"message":"Upstream gateway error"}}`)
					return nil, c.GeminiMappedError(provider, http.StatusInternalServerError, upstreamReqID, genericBody)
				}
				msg400 := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
				googleConfigError := resp.StatusCode == http.StatusBadRequest && upstream.IsGoogleProjectConfigError(msg400)
				defaultFailover := googleConfigError || s.shouldFailoverGeminiUpstreamError(resp.StatusCode)
				if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, defaultFailover) {
					upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
					upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
					upstreamDetail := ""
					if s.Options.Configured && s.Options.LogErrorBody {
						maxBytes := s.Options.LogErrorBodyMaxBytes
						if maxBytes <= 0 {
							maxBytes = 2048
						}
						upstreamDetail = logredact.TruncateUTF8(string(respBody), maxBytes)
					}
					c.Observe(ops.OpsUpstreamErrorEvent{
						Platform: provider.Record.Platform,

						ProviderID: provider.Record.ID,

						ProviderName: provider.Record.Name,

						UpstreamStatusCode: resp.StatusCode,

						UpstreamRequestID: upstreamReqID,

						Kind: "failover",

						Message: upstreamMsg,

						Detail: upstreamDetail,
					})
					if googleConfigError {
						log.Printf("[Gemini] status=400 google_config_error failover=true upstream_message=%q provider=%d", upstreamMsg, provider.Record.ID)
					}
					return nil, &forwardcore.UpstreamFailoverError{
						StatusCode: resp.StatusCode,

						ResponseBody: respBody,

						RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
					}
				}
				return nil, c.GeminiMappedError(provider, resp.StatusCode, upstreamReqID, respBody)
			}

			requestID = resp.Header.Get(requestIDHeader)
			if requestID == "" {
				requestID = resp.Header.Get("x-goog-request-id")
			}
			if requestID != "" {
				c.Header("x-request-id", requestID)
			}

			return nil, nil
		}()
		stopped = resp.StatusCode >= 400 || callbackErr != nil || compatibilityResult != nil
		return stopped, callbackErr
	}
	result, executeErr := (gemininative.Executor{}).Execute(ctx, upstream.AttemptInput{
		Protocol:      protocolcore.ProtocolAnthropicMessages,
		Body:          body,
		Stream:        req.Stream,
		ResponseModel: originalModel,
		Target:        target,
	}, c.Sink())
	if stopped {
		return compatibilityResult, executeErr
	}
	if executeErr != nil {
		return nil, executeErr
	}
	requestID = result.RequestID
	usage := &result.Usage
	firstTokenMs := result.FirstTokenMs

	// Resolve image count and size for billing.
	imageInputSize := s.extractImageInputSize(body)
	imageSize := media.NormalizeImageSizeTier(imageInputSize)
	imageCount := c.imageCount(originalModel, mappedModel)

	return &forwardcore.MessagesResult{
		RequestID: requestID,

		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,

		Usage: *usage,

		Model: originalModel,

		UpstreamModel: mappedModel,

		Stream: req.Stream,

		Duration: result.Duration,

		FirstTokenMs: firstTokenMs,

		ImageCount: imageCount,

		ImageSize: imageSize,

		ImageInputSize: imageInputSize,
	}, nil
}

func (s *Gemini) ForwardNative(ctx context.Context, output Output, provider *gatewayprovider.ExecutionProvider, originalModel string, action string, stream bool, body []byte) (*forwardcore.MessagesResult, error) {
	c := &attempt{Output: output}

	c.Images = 0
	startTime := time.Now()

	if strings.TrimSpace(originalModel) == "" {
		return nil, c.GoogleError(http.StatusBadRequest, "Missing model in URL")
	}
	if strings.TrimSpace(action) == "" {
		return nil, c.GoogleError(http.StatusBadRequest, "Missing action in URL")
	}
	if len(body) == 0 {
		return nil, c.GoogleError(http.StatusBadRequest, "Request body is empty")
	}

	// Gemini rejects messages with empty parts.
	if filteredBody, err := gemini.FilterEmptyParts(body); err == nil {
		body = filteredBody
	}

	switch action {
	case "generateContent", "streamGenerateContent", "countTokens":
		// ok
	default:
		return nil, c.GoogleError(http.StatusNotFound, "Unsupported action: "+action)
	}

	// Supply the compatibility sentinel for function calls missing a thought signature.
	body = ensureGeminiFunctionCallThoughtSignatures(body)

	// Resolve the group-mapped model through provider mappings for every credential type.
	mappedModel := providercore.ResolveForwardMappedModel(gatewayprovider.ExecutionRecord(provider), originalModel, provideradapter.ModelDefaults())
	attributes, err := gatewayprovider.ResolveModelAttributes(ctx, provider, mappedModel, s.Attributes, "gemini_generate_content")
	if err != nil {
		return nil, err
	}
	body, err = applyGeminiModelParameters(body, body, attributes, false)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	useUpstreamStream := stream
	upstreamAction := action
	if provider.Record.Type == capability.ProviderTypeOAuth && !stream && action == "generateContent" && strings.TrimSpace(provider.View().GetCredential("project_id")) != "" {
		// Collect the upstream stream because Code Assist may return an empty non-streaming response.
		useUpstreamStream = true
		upstreamAction = "streamGenerateContent"
	}
	forceAIStudio := action == "countTokens"

	requestIDHeader := "x-request-id"
	switch provider.Record.Type {
	case capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth, capability.ProviderTypeServiceAccount:
	default:
		return nil, c.GoogleError(http.StatusBadGateway, "Unsupported provider type: "+provider.Record.Type)
	}
	plan := s.geminiRequestPlan(provider, mappedModel, upstreamAction, true, stream, useUpstreamStream, forceAIStudio)
	buildReq := func(ctx context.Context) (*http.Request, string, error) {
		return gemininative.BuildRequest(ctx, body, plan)
	}

	options := s.geminiExchangeOptions(c, ctx, provider, mappedModel, geminiExchangeNative, gemininative.OpenAICompatChatCompletions)
	options.Build = buildReq
	options.RequestIDHeader = requestIDHeader
	options.Do = func(req *http.Request) (*http.Response, error) {
		return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
	}
	options.CountFallback = action == "countTokens"
	options.EstimateCount = func() int { return gemininative.EstimateGeminiCountTokens(body) }
	var requestID string
	var compatibilityResult *forwardcore.MessagesResult
	stopped := false
	target := &gemininative.Target{
		ProviderID:     provider.Record.ID,
		Model:          mappedModel,
		Mode:           gemininative.NativeResponse,
		Exchange:       options,
		Response:       s.geminiResponseAdapter(c).Options,
		StartedAt:      startTime,
		UpstreamStream: useUpstreamStream,
		OAuth:          provider.Record.Type == capability.ProviderTypeOAuth,
		Enter:          s.Enter,
	}
	target.BeforeResponse = func(ctx context.Context, resp *http.Response, requestIDHeader string) (bool, error) {
		var callbackErr error
		compatibilityResult, callbackErr = func() (*forwardcore.MessagesResult, error) {
			requestID = resp.Header.Get(requestIDHeader)
			if requestID == "" {
				requestID = resp.Header.Get("x-goog-request-id")
			}
			if requestID != "" {
				c.Header("x-request-id", requestID)
			}

			isOAuth := provider.Record.Type == capability.ProviderTypeOAuth

			if resp.StatusCode >= 400 {
				respBody := s.readUpstreamErrorBody(resp)
				// Estimate countTokens when OAuth lacks AI Studio scope.
				// This check precedes custom error policy evaluation.
				if action == "countTokens" && isOAuth && gemininative.IsGeminiInsufficientScope(resp.Header, respBody) {
					estimated := gemininative.EstimateGeminiCountTokens(body)
					c.Count(estimated)
					return &forwardcore.MessagesResult{
						RequestID: requestID,

						UpstreamHeaders: resp.Header,

						Usage: upstream.TokenUsage{},

						Model: originalModel,

						UpstreamModel: mappedModel,

						Stream: false,

						Duration: time.Since(startTime),

						FirstTokenMs: nil,
					}, nil
				}

				decision := s.applyGeminiUpstreamErrorPolicy(ctx, provider, resp.StatusCode, resp.Header, respBody, mappedModel)
				if decision.Policy == providercore.ErrorPolicyCustomSkipped || decision.Policy == providercore.ErrorPolicyPoolBypassed {
					if failoverErr := s.skippedErrorPolicyFailoverError(c, provider, resp.StatusCode, respBody, requestID); failoverErr != nil {
						return nil, failoverErr
					}
					if decision.Policy == providercore.ErrorPolicyCustomSkipped {
						return nil, c.GeminiCustomCodeSkippedError(provider, resp.StatusCode, requestID, respBody, func() {
							_ = c.GoogleError(http.StatusInternalServerError, geminiCustomCodeSkippedClientMessage)
						})
					}
					return nil, c.GeminiNativeUpstreamError(provider, resp, respBody, requestID, isOAuth)
				}
				if decision.ShouldReturnGenericError() {
					_ = c.GoogleError(http.StatusInternalServerError, "Upstream gateway error")
					return nil, fmt.Errorf("gemini upstream error: %d (not in custom error codes)", resp.StatusCode)
				}
				msg400 := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
				googleConfigError := resp.StatusCode == http.StatusBadRequest && upstream.IsGoogleProjectConfigError(msg400)
				defaultFailover := googleConfigError || s.shouldFailoverGeminiUpstreamError(resp.StatusCode)
				if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, defaultFailover) {
					evBody := gemininative.UnwrapIfNeeded(isOAuth, respBody)
					upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(evBody))
					upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
					upstreamDetail := ""
					if s.Options.Configured && s.Options.LogErrorBody {
						maxBytes := s.Options.LogErrorBodyMaxBytes
						if maxBytes <= 0 {
							maxBytes = 2048
						}
						upstreamDetail = logredact.TruncateUTF8(string(evBody), maxBytes)
					}
					c.Observe(ops.OpsUpstreamErrorEvent{
						Platform: provider.Record.Platform,

						ProviderID: provider.Record.ID,

						ProviderName: provider.Record.Name,

						UpstreamStatusCode: resp.StatusCode,

						UpstreamRequestID: requestID,

						Kind: "failover",

						Message: upstreamMsg,

						Detail: upstreamDetail,
					})
					if googleConfigError {
						log.Printf("[Gemini] status=400 google_config_error failover=true upstream_message=%q provider=%d", upstreamMsg, provider.Record.ID)
					}
					return nil, &forwardcore.UpstreamFailoverError{
						StatusCode: resp.StatusCode,

						ResponseBody: evBody,

						RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
					}
				}

				return nil, c.GeminiNativeUpstreamError(provider, resp, respBody, requestID, isOAuth)
			}

			return nil, nil
		}()
		stopped = resp.StatusCode >= 400 || callbackErr != nil || compatibilityResult != nil
		return stopped, callbackErr
	}
	result, executeErr := (gemininative.Executor{}).Execute(ctx, upstream.AttemptInput{
		Protocol:      protocolcore.ProtocolGeminiGenerateContent,
		Body:          body,
		Stream:        stream,
		ResponseModel: originalModel,
		Target:        target,
	}, c.Sink())
	if stopped {
		return compatibilityResult, executeErr
	}
	if executeErr != nil {
		return nil, executeErr
	}
	if result.EstimatedTokenCount != nil {
		return &forwardcore.MessagesResult{
			Usage:         upstream.TokenUsage{},
			Model:         originalModel,
			UpstreamModel: mappedModel,
			Stream:        false,
			Duration:      result.Duration,
		}, nil
	}
	requestID = result.RequestID
	usage := &result.Usage
	firstTokenMs := result.FirstTokenMs

	// Resolve image count and size for billing.
	imageInputSize := s.extractImageInputSize(body)
	imageSize := media.NormalizeImageSizeTier(imageInputSize)
	imageCount := c.imageCount(originalModel, mappedModel)

	return &forwardcore.MessagesResult{
		RequestID: requestID,

		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,

		Usage: *usage,

		Model: originalModel,

		UpstreamModel: mappedModel,

		Stream: result.Stream,

		Duration: result.Duration,

		FirstTokenMs: firstTokenMs,

		ImageCount: imageCount,

		ImageSize: imageSize,

		ImageInputSize: imageInputSize,
	}, nil
}

// checkErrorPolicyInLoop evaluates error policy before retrying.
// A match tells the caller to stop retrying. The returned response body is readable
// after policy inspection; responses that need no inspection are returned unchanged.
func (s *Gemini) checkErrorPolicyInLoop(
	ctx context.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, mappedModel string,
) (matched bool, rebuilt *http.Response) {
	if resp.StatusCode < 400 || s.Health == nil {
		return false, resp
	}
	body := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	rebuilt = &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	policy := s.Health.CheckErrorPolicy(ctx, gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(ctx, resp.StatusCode, nil, body, []string{mappedModel}))
	if policy == providercore.ErrorPolicyTempUnscheduled {
		// Mark the temporary scheduling policy as applied
		// so final error handling does not repeat the database update.
		rebuilt.Header.Set(geminiAppliedTempPolicyHeader, "1")
	}
	// Pool retries use the handler budget; do not add Gemini retries after a policy match.
	return policy != providercore.ErrorPolicyNone, rebuilt
}

func (s *Gemini) shouldRetryGeminiUpstreamError(provider *gatewayprovider.ExecutionProvider, statusCode int) bool {
	switch statusCode {
	case 429, 500, 502, 503, 504, 529:
		return true
	case 403:
		// Code Assist activation or quota propagation can produce a transient 403.
		if provider == nil || provider.Record.Type != capability.ProviderTypeOAuth {
			return false
		}
		oauthType := strings.ToLower(strings.TrimSpace(provider.View().GetCredential("oauth_type")))
		if oauthType == "" && strings.TrimSpace(provider.View().GetCredential("project_id")) != "" {
			// Older Code Assist records identify the mode through project_id alone.
			oauthType = "code_assist"
		}
		return oauthType == "code_assist"
	default:
		return false
	}
}

func (s *Gemini) shouldFailoverGeminiUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

// skippedErrorPolicyFailoverError permits failover when custom policy skips health changes.
// Pool mode allows same-provider retry only for configured statuses.
func (s *Gemini) skippedErrorPolicyFailoverError(c *attempt, provider *gatewayprovider.ExecutionProvider, statusCode int, respBody []byte, upstreamRequestID string) *forwardcore.UpstreamFailoverError {
	if !s.shouldFailoverGeminiUpstreamError(statusCode) {
		return nil
	}
	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
	c.Observe(ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform,

		ProviderID: provider.Record.ID,

		ProviderName: provider.Record.Name,

		UpstreamStatusCode: statusCode,

		UpstreamRequestID: upstreamRequestID,

		Kind: "failover",

		Message: upstreamMsg,

		Detail: s.upstreamErrorDetail(respBody),
	})
	return &forwardcore.UpstreamFailoverError{
		StatusCode: statusCode,

		ResponseBody: respBody,

		RetryableOnSameProvider: provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(statusCode),
	}
}

// upstreamErrorDetail truncates the upstream error body according to logging settings.
func (s *Gemini) upstreamErrorDetail(body []byte) string {
	if s == nil || !s.Options.Configured || !s.Options.LogErrorBody {
		return ""
	}
	maxBytes := s.Options.LogErrorBodyMaxBytes
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	return logredact.TruncateUTF8(string(body), maxBytes)
}

func (s *Gemini) ForwardAIStudioGET(ctx context.Context, provider *gatewayprovider.ExecutionProvider, path string) (*gemininative.HTTPResult, error) {
	if provider == nil {
		return nil, errors.New("provider is nil")
	}
	options := gemininative.ModelGetOptions{
		Mode:        gemininative.CredentialMode(provider.Record.Type),
		BaseURL:     func() string { return provider.View().GetGeminiBaseURL(geminicli.AIStudioBaseURL) },
		APIKey:      func() string { return provider.View().GetCredential("api_key") },
		ValidateURL: s.validateUpstreamBaseURL,
		Enter:       s.Enter,
		Do: func(req *http.Request) (*http.Response, error) {
			proxy := ""
			if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
				proxy = provider.Record.Proxy.URL()
			}
			return s.Transport.Do(req, proxy, provider.Record.ID, provider.Record.Concurrency)
		},
		FilterHeaders: func(header http.Header) http.Header {
			return egressadapter.FilterHeaders(header, s.HeaderFilter)
		},
	}
	if s.Tokens != nil {
		options.Token = func(ctx context.Context) (string, error) {
			return gatewayprovider.ExecutionToken(ctx, s.Tokens, provider)
		}
	}
	return gemininative.ReadAIStudioModel(ctx, path, options)
}

// applyGeminiUpstreamErrorPolicy applies custom and default handling across Gemini entry points.
// Pool bypass skips the default observer so a 429 does not create a local cooldown.
func (s *Gemini) applyGeminiUpstreamErrorPolicy(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	statusCode int,
	headers http.Header,
	body []byte,
	mappedModel string,
) providercore.UpstreamErrorDecision {
	decision := providercore.ErrorDecisionWithoutPersistence(gatewayprovider.ExecutionErrorPolicy(provider), statusCode)
	if s == nil || provider == nil {
		return decision
	}
	if headers != nil && headers.Get(geminiAppliedTempPolicyHeader) == "1" {
		headers.Del(geminiAppliedTempPolicyHeader)
		decision.Policy = providercore.ErrorPolicyTempUnscheduled
		decision.StopScheduling = true
		return decision
	}
	if s.Health != nil {
		decision.Policy = s.Health.ApplyExplicitErrorPolicy(ctx, gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(ctx, statusCode, nil, body, []string{mappedModel}))
		decision.StopScheduling = decision.Policy == providercore.ErrorPolicyCustomMatched || decision.Policy == providercore.ErrorPolicyTempUnscheduled
	}
	switch decision.Policy {
	case providercore.ErrorPolicyCustomMatched, providercore.ErrorPolicyTempUnscheduled:
		decision.StopScheduling = true
		return decision
	case providercore.ErrorPolicyCustomSkipped, providercore.ErrorPolicyPoolBypassed:
		return decision
	}
	s.observeHealth(ctx, provider, statusCode, headers, body)
	return decision
}

// ensureGeminiFunctionCallThoughtSignatures fills missing function-call signatures through the protocol adapter.
func ensureGeminiFunctionCallThoughtSignatures(body []byte) []byte {
	return bridge.NativeEnsureGeminiFunctionCallThoughtSignatures(bridge.NativeGeminiOptions{DummyThoughtSignature: geminiDummyThoughtSignature}, body)
}

// convertClaudeMessagesToGeminiGenerateContent converts a Claude body using the Gemini protocol adapter.
func convertClaudeMessagesToGeminiGenerateContent(body []byte) ([]byte, error) {
	return bridge.NativeConvertClaudeMessagesToGeminiGenerateContent(bridge.NativeGeminiOptions{DummyThoughtSignature: geminiDummyThoughtSignature}, body)
}

func (s *Gemini) extractImageInputSize(body []byte) string {
	var req struct {
		GenerationConfig *struct {
			ImageConfig *struct {
				ImageSize string `json:"imageSize"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.GenerationConfig != nil && req.GenerationConfig.ImageConfig != nil {
		return strings.TrimSpace(req.GenerationConfig.ImageConfig.ImageSize)
	}

	return ""
}

// observeHealth evaluates the response and copies updated credentials and extra state into the execution target.
func (s *Gemini) observeHealth(ctx context.Context, target *gatewayprovider.ExecutionProvider, status int, headers http.Header, body []byte) {
	record := gatewayprovider.ExecutionRecord(target)
	updated := s.Errors.Observe(ctx, record, status, headers, body, gatewayprovider.HealthObservationFromContext(ctx, status, headers, body, nil))
	if target != nil && updated != nil {
		target.Record.Credentials, target.Record.Extra = updated.Credentials, updated.Extra
	}
}
