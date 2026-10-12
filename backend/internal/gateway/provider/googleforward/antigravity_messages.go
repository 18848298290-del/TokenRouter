package googleforward

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Forward converts Claude requests to Gemini and executes them through Antigravity.
//
// The shared retry adapter handles provider cooldowns and bounded retries.
// Provider-switch signals become UpstreamFailoverError for the caller.
//
// @project-doc docs/interfaces/antigravity_upstream.md#antigravity_native_execution
func (s *Antigravity) Forward(ctx context.Context, output Output, provider *gatewayprovider.ExecutionProvider, body []byte, isStickySession bool) (*forwardcore.MessagesResult, error) {
	c := &attempt{Output: output}

	startTime := time.Now()

	sessionID := c.GetHeader("session_id")
	prefix := logPrefix(sessionID, provider.Record.Name)

	var claudeReq protocolanthropic.ClaudeRequest
	if err := json.Unmarshal(body, &claudeReq); err != nil {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", "Invalid request body")
	}
	if strings.TrimSpace(claudeReq.Model) == "" {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", "Missing model")
	}

	originalModel := claudeReq.Model
	// Include thinking in final-model resolution so eligibility and execution use the same model.
	thinkingEnabled := claudeReq.Thinking != nil && (claudeReq.Thinking.Type == "enabled" || claudeReq.Thinking.Type == "adaptive")
	modelCtx := requeststate.WithThinkingEnabled(ctx, thinkingEnabled)
	mappedModel := gatewayprovider.ExecutionModelPolicy(provider).FinalAntigravityModel(modelCtx, claudeReq.Model)
	if mappedModel == "" {
		c.FeatureDenied()
		return nil, c.ClaudeError(http.StatusForbidden, "permission_error", fmt.Sprintf("model %s not in whitelist", claudeReq.Model))
	}
	billingModel := mappedModel

	if s.Tokens == nil {
		return nil, c.ClaudeError(http.StatusBadGateway, "api_error", "Antigravity token provider not configured")
	}
	accessToken, err := gatewayprovider.ExecutionToken(ctx, s.Tokens, provider)
	if err != nil {
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,

			ResponseBody: []byte(`{"error":{"type":"authentication_error","message":"Failed to get upstream access token"},"type":"error"}`),
		}
	}

	projectID, err := resolveAntigravityProjectID(provider)
	if err != nil {
		_ = c.ClaudeError(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	// Antigravity requests include the configured identity prompt.
	transformOpts := s.getClaudeTransformOptions(ctx)
	attributes, attributesErr := gatewayprovider.ResolveModelAttributes(ctx, provider, mappedModel, s.Attributes, "gemini_generate_content")
	if attributesErr != nil {
		return nil, attributesErr
	}
	transformOpts.Capabilities = &attributes
	transformOpts.Thinking, err = gatewayprovider.GeminiThinking(attributes, body)
	if err != nil {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	transformOpts.ThinkingResolved = true
	transformOpts.EnableIdentityPatch = true // Required by the Antigravity request format.

	geminiBody, err := antigravity.TransformClaudeToGeminiWithOptions(&claudeReq, projectID, mappedModel, transformOpts)
	if err != nil {
		return nil, c.ClaudeError(http.StatusBadRequest, "invalid_request_error", "Invalid request")
	}

	// Use the upstream streaming endpoint for both client response modes.
	// Non-streaming clients receive the collected and converted stream.
	action := "streamGenerateContent"

	retry, params := s.antigravityRetryAdapter(antigravityRetryLoopParams{
		ctx: ctx,

		prefix: prefix,

		provider: provider,

		proxyURL: proxyURL,

		accessToken: accessToken,

		action: action,

		body: geminiBody,

		c: c,

		httpUpstream: s.Transport,

		providerRepo: s.Store,

		handleError: s.handleUpstreamError,

		requestedModel: originalModel,

		isStickySession: isStickySession, // The caller determines whether this request is sticky.

		groupID: 0, // The caller clears sticky state because this entry has no group ID.

		sessionHash: "", // The caller owns the session binding.

	})
	target := &antigravity.Target{
		ProviderID: provider.Record.ID,
		Model:      billingModel,
		Mode:       antigravity.ModeClaudeResponse,
		StartedAt:  startTime,
		Response:   s.antigravityResponseAdapter(c).Options,
		Enter:      s.Enter,
	}
	target.Exchange = func(context.Context) (*http.Response, error) {
		result, err := retry.AntigravityRetryLoop(params)
		if err != nil {
			// Convert the switch signal so the handler can select another provider.
			if switchErr, ok := antigravity.IsAntigravityProviderSwitchError(err); ok {
				return nil, &forwardcore.UpstreamFailoverError{
					StatusCode: http.StatusServiceUnavailable,

					ForceCacheBilling: switchErr.IsStickySession,
				}
			}
			// Report client cancellation separately from upstream failure.
			if c.RequestContext().Err() != nil {
				return nil, c.ClaudeError(http.StatusBadGateway, "client_disconnected", "Client disconnected before upstream response")
			}
			return nil, c.ClaudeError(http.StatusBadGateway, "upstream_error", "Upstream request failed after retries")
		}
		options := antigravity.ClaudeRecoveryOptions{
			Retry: func(body []byte) (*http.Response, error) {
				next := params
				next.Body = body
				value, err := retry.AntigravityRetryLoop(next)
				if err != nil {
					return nil, err
				}
				return value.Resp, nil
			},
			SignatureEnabled:   s.Gateway.IsSignatureRectifierEnabled,
			BudgetEnabled:      s.Gateway.IsBudgetRectifierEnabled,
			TransformOptions:   func(context.Context) antigravity.TransformOptions { return transformOpts },
			LogConfig:          s.getLogConfig,
			ErrorDetail:        s.getUpstreamErrorDetail,
			ReadErrorBody:      s.readUpstreamErrorBody,
			Observe:            retry.Options.Observe,
			IsBudgetConstraint: anthropic.IsThinkingBudgetConstraintError,
			BudgetTokens:       anthropic.BudgetRectifyBudgetTokens,
			MinMaxTokens:       anthropic.BudgetRectifyMinMaxTokens,
			MaxTokens:          anthropic.BudgetRectifyMaxTokens,
			TruncateForLog:     logredact.TruncateLine,
			TruncateString:     logredact.TruncateUTF8,
		}
		return antigravity.RecoverClaude(ctx, antigravity.ClaudeRecoveryInput{
			ProviderID:     provider.Record.ID,
			ProviderName:   provider.Record.Name,
			Prefix:         prefix,
			ProjectID:      projectID,
			Model:          mappedModel,
			Request:        claudeReq,
			InitialOptions: transformOpts,
		}, result.Resp, options), nil
	}
	target.BeforeResponse = func(ctx context.Context, resp *http.Response) (bool, error) {
		if resp.StatusCode < 400 {
			return false, nil
		}
		respBody := s.readUpstreamErrorBody(resp)
		if resp.StatusCode >= 400 {
			// Return a typed prompt-length error for the caller's fallback path.
			if resp.StatusCode == http.StatusBadRequest && antigravity.IsPromptTooLongError(respBody) {
				upstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
				upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
				upstreamDetail := s.getUpstreamErrorDetail(respBody)
				logBody, maxBytes := s.getLogConfig()
				if logBody {
					logging.LegacyPrintf("service.antigravity_gateway", "%s status=400 prompt_too_long=true upstream_message=%q request_id=%s body=%s", prefix, upstreamMsg, resp.Header.Get("x-request-id"), logredact.TruncateLine(respBody, maxBytes))
				}
				c.Observe(ops.OpsUpstreamErrorEvent{
					Platform: provider.Record.Platform,

					ProviderID: provider.Record.ID,

					ProviderName: provider.Record.Name,

					UpstreamStatusCode: resp.StatusCode,

					UpstreamRequestID: resp.Header.Get("x-request-id"),

					Kind: "prompt_too_long",

					Message: upstreamMsg,

					Detail: upstreamDetail,
				})
				return true, &antigravity.PromptTooLongError{
					StatusCode: resp.StatusCode,

					RequestID: resp.Header.Get("x-request-id"),

					Body: respBody,
				}
			}

			s.handleUpstreamError(ctx, prefix, provider, resp.StatusCode, resp.Header, respBody, originalModel, 0, "", isStickySession)

			// Recognized project configuration errors allow same-provider retry and failover.
			if resp.StatusCode == http.StatusBadRequest {
				msg := strings.ToLower(strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody)))
				if upstream.IsGoogleProjectConfigError(msg) {
					upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody)))
					upstreamDetail := s.getUpstreamErrorDetail(respBody)
					log.Printf("%s status=400 google_config_error failover=true upstream_message=%q provider=%d", prefix, upstreamMsg, provider.Record.ID)
					c.Observe(ops.OpsUpstreamErrorEvent{
						Platform: provider.Record.Platform,

						ProviderID: provider.Record.ID,

						ProviderName: provider.Record.Name,

						UpstreamStatusCode: resp.StatusCode,

						UpstreamRequestID: resp.Header.Get("x-request-id"),

						Kind: "failover",

						Message: upstreamMsg,

						Detail: upstreamDetail,
					})
					return true, &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, RetryableOnSameProvider: true}
				}
			}

			if s.shouldFailoverUpstreamError(resp.StatusCode) {
				upstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
				upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
				upstreamDetail := s.getUpstreamErrorDetail(respBody)
				c.Observe(ops.OpsUpstreamErrorEvent{
					Platform: provider.Record.Platform,

					ProviderID: provider.Record.ID,

					ProviderName: provider.Record.Name,

					UpstreamStatusCode: resp.StatusCode,

					UpstreamRequestID: resp.Header.Get("x-request-id"),

					Kind: "failover",

					Message: upstreamMsg,

					Detail: upstreamDetail,
				})
				return true, &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody}
			}

			return true, c.MappedClaudeError(provider, resp.StatusCode, resp.Header.Get("x-request-id"), respBody)
		}
		return false, nil
	}
	target.OutputError = func(err error) {
		kind := "stream_collect_error"
		if claudeReq.Stream {
			kind = "stream_error"
		}
		logging.LegacyPrintf("service.antigravity_gateway", "%s status=%s error=%v", prefix, kind, err)
	}
	result, err := (antigravity.Executor{}).Execute(ctx, upstream.AttemptInput{
		Protocol:      protocol.ProtocolAnthropicMessages,
		Body:          geminiBody,
		ResponseModel: originalModel,
		Stream:        claudeReq.Stream,
		Target:        target,
	}, c.Sink())
	if err != nil {
		return nil, err
	}
	return &forwardcore.MessagesResult{
		RequestID:             result.RequestID,
		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,
		Usage:                 result.Usage,
		Model:                 originalModel,
		UpstreamModel:         billingModel,
		Stream:                claudeReq.Stream,
		Duration:              result.Duration,
		FirstTokenMs:          result.FirstTokenMs,
		ClientDisconnect:      result.ClientDisconnect,
	}, nil
}
