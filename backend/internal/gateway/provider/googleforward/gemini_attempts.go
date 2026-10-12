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

	// Gemini 的 functionCall 需要 thoughtSignature；缺少时沿用既有占位签名。
	// 协议说明：https://ai.google.dev/gemini-api/docs/thought-signatures
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

// Forward 准备一次 Messages 到 Gemini 的执行，提供商切换由外层网关决定。
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
	// 所有 Gemini 提供商类型都执行提供商模型映射，OAuth 也必须与调度和可见模型解析保持一致。
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

	// 图片生成计费
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

	// 过滤掉 parts 为空的消息（Gemini API 不接受空 parts）
	if filteredBody, err := gemini.FilterEmptyParts(body); err == nil {
		body = filteredBody
	}

	switch action {
	case "generateContent", "streamGenerateContent", "countTokens":
		// ok
	default:
		return nil, c.GoogleError(http.StatusNotFound, "Unsupported action: "+action)
	}

	// 补齐 functionCall 的既有占位签名，保留上游严格校验下的兼容行为。
	body = ensureGeminiFunctionCallThoughtSignatures(body)

	// 分组映射后的模型进入提供商后统一解析为最终上游模型，不按凭据类型跳过。
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
		// Code Assist 的非流响应可能为空，沿用流式上游收集后返回的方式。
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
				// OAuth 缺少 AI Studio scope 时保留计数估算回退；
				// 这项检查先于错误策略，不受自定义错误码影响。
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

	// 图片生成计费
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

// checkErrorPolicyInLoop 在重试循环内预检查错误策略。
// 返回 true 表示策略已匹配（调用者应 break），resp 已重建可直接使用。
// 返回 false 表示 ErrorPolicyNone，resp 已重建，调用者继续走重试逻辑。
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
		// CheckErrorPolicy 已写入临时不可调度状态，给最终错误处理留下内部标记，
		// 避免同一个响应再次执行规则并重复写库。
		rebuilt.Header.Set(geminiAppliedTempPolicyHeader, "1")
	}
	// 池模式由 handler 层按提供商配置的重试预算处理，不能再叠加 Gemini 固定内部重试。
	return policy != providercore.ErrorPolicyNone, rebuilt
}

func (s *Gemini) shouldRetryGeminiUpstreamError(provider *gatewayprovider.ExecutionProvider, statusCode int) bool {
	switch statusCode {
	case 429, 500, 502, 503, 504, 529:
		return true
	case 403:
		// Code Assist 的激活或配额传播可能短暂返回 403，保持原重试资格。
		if provider == nil || provider.Record.Type != capability.ProviderTypeOAuth {
			return false
		}
		oauthType := strings.ToLower(strings.TrimSpace(provider.View().GetCredential("oauth_type")))
		if oauthType == "" && strings.TrimSpace(provider.View().GetCredential("project_id")) != "" {
			// 兼容只保存 project_id 的历史 Code Assist 提供商。
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

// skippedErrorPolicyFailoverError 处理 ErrorPolicyCustomSkipped：跳过提供商状态写入不等于跳过换号。
// 可切换的状态码返回 UpstreamFailoverError；池模式仅对配置的状态允许同提供商重试。
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

// upstreamErrorDetail 按配置截断上游错误体，用于运维日志。
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

// applyGeminiUpstreamErrorPolicy 统一 Gemini 三种协议入口的显式策略和默认状态处理。
// 池模式绕过时绝不能继续调用 handleGeminiUpstreamError，否则 429 会写入本地限流。
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

// ensureGeminiFunctionCallThoughtSignatures 调用 Gemini 协议函数补充函数调用的 thought signature。
func ensureGeminiFunctionCallThoughtSignatures(body []byte) []byte {
	return bridge.NativeEnsureGeminiFunctionCallThoughtSignatures(bridge.NativeGeminiOptions{DummyThoughtSignature: geminiDummyThoughtSignature}, body)
}

// convertClaudeMessagesToGeminiGenerateContent 调用 Gemini 协议转换函数生成请求体。
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

// observeHealth 执行健康观测并将凭据和附加状态写回执行提供商。
func (s *Gemini) observeHealth(ctx context.Context, target *gatewayprovider.ExecutionProvider, status int, headers http.Header, body []byte) {
	record := gatewayprovider.ExecutionRecord(target)
	updated := s.Errors.Observe(ctx, record, status, headers, body, gatewayprovider.HealthObservationFromContext(ctx, status, headers, body, nil))
	if target != nil && updated != nil {
		target.Record.Credentials, target.Record.Extra = updated.Credentials, updated.Extra
	}
}
