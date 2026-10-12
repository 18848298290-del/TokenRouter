package googleforward

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	protocolforward "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

const (
	antigravityCompatChatCompletions antigravityCompatProtocol = iota
	antigravityCompatResponses
)

type antigravityModelAttributes struct {
	Resolution *modelcatalog.Resolution
	Controls   []byte
}

type antigravityCompatProtocol uint8

type antigravityCompatRequest struct {
	protocol          antigravityCompatProtocol
	originalBody      []byte
	claudeBody        []byte
	originalModel     string
	clientStream      bool
	includeUsage      bool
	startTime         time.Time
	reasoningEffort   *string
	clientToolMapping protocolbridge.ResponsesClientToolMapping
}

type antigravityCompatUpstreamCall struct {
	request      antigravityCompatRequest
	ctx          context.Context
	billingModel string
	prefix       string
	proxyURL     string
	accessToken  string
	geminiBody   []byte
}

// ForwardAsChatCompletions forwards Chat Completions through an Antigravity OAuth provider.
func (s *Antigravity) ForwardAsChatCompletions(
	ctx context.Context,
	output Output,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	_ *requeststate.ParsedRequest,
) (*protocolforward.MessagesResult, error) {
	ctx = requeststate.WithClientProtocol(ctx, protocol.ProtocolOpenAIChatCompletions)
	c := &attempt{Output: output}

	if err := s.validateAntigravityCompatProvider(c, provider); err != nil {
		return nil, err
	}

	var request protocolopenai.ChatCompletionsRequest
	if json.Unmarshal(body, &request) != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	responsesRequest, err := protocolbridge.ChatCompletionsToResponses(&request, protocolforward.ConversionOptionsForModel(request.Model))
	if err != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	claudeRequest, err := protocolbridge.ResponsesToAnthropicRequest(responsesRequest)
	if err != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	antigravity.PreserveChatCompletionTokenLimit(&request, claudeRequest)
	claudeRequest.Stream = request.Stream
	claudeBody, err := json.Marshal(claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	claudeBody = prepareAntigravityCompatTools(c, claudeBody)

	return s.forwardAntigravityCompat(ctx, c, provider, antigravityCompatRequest{
		protocol: antigravityCompatChatCompletions,

		originalBody: body,

		claudeBody: claudeBody,

		originalModel: request.Model,

		clientStream: request.Stream,

		includeUsage: request.StreamOptions != nil && request.StreamOptions.IncludeUsage,

		startTime: time.Now(),

		reasoningEffort: protocolforward.ExtractEffort(body, true, capability.NormalizeRecordedOpenAIEffortForModel),
	})
}

// ForwardAsResponses forwards Responses through an Antigravity OAuth provider.
func (s *Antigravity) ForwardAsResponses(
	ctx context.Context,
	output Output,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	_ *requeststate.ParsedRequest,
) (*protocolforward.MessagesResult, error) {
	ctx = requeststate.WithClientProtocol(ctx, protocol.ProtocolOpenAIResponses)
	c := &attempt{Output: output}

	if err := s.validateAntigravityCompatProvider(c, provider); err != nil {
		return nil, err
	}

	adaptedBody, clientToolMapping, err := protocolforward.AdaptResponsesClientToolsForAnthropic(body)
	if err != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}

	var request protocolopenai.ResponsesRequest
	if json.Unmarshal(adaptedBody, &request) != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	claudeRequest, err := protocolbridge.ResponsesToAnthropicRequest(&request)
	if err != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	claudeRequest.Stream = request.Stream
	claudeBody, err := json.Marshal(claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	claudeBody = prepareAntigravityCompatTools(c, claudeBody)

	return s.forwardAntigravityCompat(ctx, c, provider, antigravityCompatRequest{
		protocol: antigravityCompatResponses,

		originalBody: body,

		claudeBody: claudeBody,

		originalModel: request.Model,

		clientStream: request.Stream,

		startTime: time.Now(),

		reasoningEffort: protocolforward.ExtractEffort(body, false, capability.NormalizeRecordedOpenAIEffortForModel),

		clientToolMapping: clientToolMapping,
	})
}

func (s *Antigravity) validateAntigravityCompatProvider(c *attempt, provider *gatewayprovider.ExecutionProvider) error {
	if provider != nil && provider.Record.Platform == capability.PlatformAntigravity && provider.Record.Type == capability.ProviderTypeOAuth {
		return nil
	}
	return c.AntigravityCompatError(http.StatusBadRequest,
		"invalid_request_error",
		"native OAuth provider required for antigravity compatibility mode",
	)
}

// prepareAntigravityCompatTools rewrites tool names and records the response mapping.
func prepareAntigravityCompatTools(c *attempt, body []byte) []byte {
	rewrite := anthropic.BuildToolNameRewriteFromBody(body)
	if c != nil {
		// Replace the mapping on every attempt, including attempts with no renamed tools.
		c.ToolNames = rewrite
	}
	return anthropic.ApplyToolNameRewriteToBody(body, rewrite)
}

func (s *Antigravity) forwardAntigravityCompat(
	ctx context.Context,
	c *attempt,
	provider *gatewayprovider.ExecutionProvider,
	request antigravityCompatRequest,
) (*protocolforward.MessagesResult, error) {
	call, err := s.prepareAntigravityCompatCall(ctx, c, provider, request)
	if err != nil {
		return nil, err
	}

	retry, params := s.antigravityRetryAdapter(antigravityRetryLoopParams{
		ctx: call.ctx,

		prefix: call.prefix,

		provider: provider,

		proxyURL: call.proxyURL,

		accessToken: call.accessToken,

		action: "streamGenerateContent",

		body: call.geminiBody,

		c: c,

		httpUpstream: s.Transport,

		providerRepo: s.Store,

		handleError: s.handleUpstreamError,

		requestedModel: request.originalModel,

		isStickySession: false,

		groupID: 0,

		sessionHash: "",
	})
	mode := antigravity.ModeChatResponse
	wireProtocol := protocol.ProtocolOpenAIChatCompletions
	if request.protocol == antigravityCompatResponses {
		mode = antigravity.ModeResponsesResponse
		wireProtocol = protocol.ProtocolOpenAIResponses
	}
	target := &antigravity.Target{
		ProviderID:   provider.Record.ID,
		Model:        call.billingModel,
		Mode:         mode,
		StartedAt:    request.startTime,
		IncludeUsage: request.includeUsage,
		ClientTools:  request.clientToolMapping,
		Response:     s.antigravityResponseAdapter(c).Options,
		Enter:        s.Enter,

		Exchange: func(context.Context) (*http.Response, error) {
			result, err := retry.AntigravityRetryLoop(params)
			if err != nil {
				return nil, s.handleAntigravityCompatTransportError(c, err)
			}
			return result.Resp, nil
		},

		BeforeResponse: func(ctx context.Context, resp *http.Response) (bool, error) {
			if resp.StatusCode >= http.StatusBadRequest {
				return true, s.handleAntigravityCompatHTTPError(ctx, c, provider, call, resp)
			}
			return false, nil
		},
	}
	result, err := (antigravity.Executor{}).Execute(call.ctx, upstream.AttemptInput{
		Protocol:      wireProtocol,
		Body:          call.geminiBody,
		ResponseModel: request.originalModel,
		Stream:        request.clientStream,
		Target:        target,
	}, c.Sink())
	if err != nil {
		return nil, err
	}
	return &protocolforward.MessagesResult{
		RequestID:             result.RequestID,
		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,
		Usage:                 result.Usage,
		Model:                 request.originalModel,
		UpstreamModel:         call.billingModel,
		Stream:                request.clientStream,
		Duration:              result.Duration,
		FirstTokenMs:          result.FirstTokenMs,
		ReasoningEffort:       request.reasoningEffort,
		ClientDisconnect:      result.ClientDisconnect,
	}, nil
}

func (s *Antigravity) prepareAntigravityCompatCall(
	ctx context.Context,
	c *attempt,
	provider *gatewayprovider.ExecutionProvider,
	request antigravityCompatRequest,
) (*antigravityCompatUpstreamCall, error) {
	var claudeRequest protocolanthropic.ClaudeRequest
	if json.Unmarshal(request.claudeBody, &claudeRequest) != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "Invalid request body")
	}

	thinkingEnabled := claudeRequest.Thinking != nil &&
		(claudeRequest.Thinking.Type == "enabled" || claudeRequest.Thinking.Type == "adaptive")
	modelCtx := requeststate.WithThinkingEnabled(ctx, thinkingEnabled)
	mappedModel := gatewayprovider.ExecutionModelPolicy(provider).FinalAntigravityModel(modelCtx, request.originalModel)
	if mappedModel == "" {
		c.FeatureDenied()
		message := fmt.Sprintf("model %s not in whitelist", request.originalModel)
		return nil, c.AntigravityCompatError(http.StatusForbidden, "permission_error", message)
	}
	if s.Tokens == nil {
		return nil, c.AntigravityCompatError(http.StatusBadGateway, "api_error", "Antigravity token provider not configured")
	}
	accessToken, err := gatewayprovider.ExecutionToken(ctx, s.Tokens, provider)
	if err != nil {
		return nil, &protocolforward.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,

			ResponseBody: []byte(`{"error":{"type":"authentication_error","message":"Failed to get upstream access token"},"type":"error"}`),
		}
	}

	projectID, err := resolveAntigravityProjectID(provider)
	if err != nil {
		_ = c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	attributes, attributesErr := gatewayprovider.ResolveModelAttributes(modelCtx, provider, mappedModel, s.Attributes, "gemini_generate_content")
	if attributesErr != nil {
		return nil, attributesErr
	}
	geminiBody, err := s.buildAntigravityCompatGeminiBody(modelCtx, request.claudeBody, &claudeRequest, projectID, mappedModel, antigravityModelAttributes{Resolution: &attributes, Controls: request.originalBody})
	if err != nil {
		return nil, c.AntigravityCompatError(http.StatusBadRequest, "invalid_request_error", "Invalid request")
	}

	request.reasoningEffort = gatewayprovider.ApplyThinkingEnabledFallback(request.reasoningEffort, request.originalBody, mappedModel)
	return &antigravityCompatUpstreamCall{
		request: request,

		ctx: modelCtx,

		billingModel: mappedModel,

		prefix: logPrefix(c.GetHeader("session_id"), provider.Record.Name),

		proxyURL: antigravityCompatProxyURL(provider),

		accessToken: accessToken,

		geminiBody: geminiBody,
	}, nil
}

func (s *Antigravity) buildAntigravityCompatGeminiBody(
	ctx context.Context,
	claudeBody []byte,
	claudeRequest *protocolanthropic.ClaudeRequest,
	projectID string,
	mappedModel string,
	inputs ...antigravityModelAttributes,
) ([]byte, error) {
	var resolved *modelcatalog.Resolution
	controls := claudeBody
	if len(inputs) > 0 {
		resolved = inputs[0].Resolution
		if inputs[0].Controls != nil {
			controls = inputs[0].Controls
		}
	}

	if strings.HasPrefix(strings.ToLower(mappedModel), "gemini-") {
		body, err := convertClaudeMessagesToGeminiGenerateContent(claudeBody)
		if err != nil {
			return nil, err
		}
		body, err = antigravity.EnableMixedGeminiToolInvocations(body)
		if err != nil {
			return nil, err
		}
		body = ensureGeminiFunctionCallThoughtSignatures(body)
		body, err = antigravity.InjectIdentityPatchToGeminiRequest(body)
		if err != nil {
			return nil, err
		}
		if cleaned, cleanErr := antigravity.CleanGeminiRequest(body); cleanErr == nil {
			body = cleaned
		}
		if resolved != nil {
			body, err = applyGeminiModelParameters(body, controls, *resolved, true)
			if err != nil {
				return nil, err
			}
		}

		return antigravity.WrapV1InternalRequest(projectID, mappedModel, body)
	}

	options := s.getClaudeTransformOptions(ctx)
	if resolved != nil {
		options.Capabilities = resolved
		var err error
		options.Thinking, err = gatewayprovider.GeminiThinking(*resolved, controls)
		if err != nil {
			return nil, err
		}
		options.ThinkingResolved = true
	}
	options.EnableIdentityPatch = true
	return antigravity.TransformClaudeToGeminiWithOptions(claudeRequest, projectID, mappedModel, options)
}

func antigravityCompatProxyURL(provider *gatewayprovider.ExecutionProvider) string {
	if provider.Record.ProxyID == nil || provider.Record.Proxy == nil {
		return ""
	}
	return provider.Record.Proxy.URL()
}

func (s *Antigravity) handleAntigravityCompatTransportError(c *attempt, err error) error {
	if switchErr, ok := antigravity.IsAntigravityProviderSwitchError(err); ok {
		return &protocolforward.UpstreamFailoverError{
			StatusCode: http.StatusServiceUnavailable,

			ForceCacheBilling: switchErr.IsStickySession,
		}
	}
	if c.RequestContext().Err() != nil {
		return c.AntigravityCompatError(http.StatusBadGateway, "client_disconnected", "Client disconnected before upstream response")
	}
	return c.AntigravityCompatError(http.StatusBadGateway, "upstream_error", "Upstream request failed after retries")
}

func (s *Antigravity) handleAntigravityCompatHTTPError(
	ctx context.Context,
	c *attempt,
	provider *gatewayprovider.ExecutionProvider,
	call *antigravityCompatUpstreamCall,
	resp *http.Response,
) error {
	body := s.readUpstreamErrorBody(resp)
	s.handleUpstreamError(
		ctx,
		call.prefix,
		provider,
		resp.StatusCode,
		resp.Header,
		body,
		call.request.originalModel,
		0,
		"",
		false,
	)
	if s.shouldFailoverUpstreamError(resp.StatusCode) {
		message := logredact.SanitizeUpstreamQueries(strings.TrimSpace(google.ExtractPlatformMessage(body)))
		event := ops.OpsUpstreamErrorEvent{
			Platform: provider.Record.Platform,

			ProviderID: provider.Record.ID,

			ProviderName: provider.Record.Name,

			UpstreamStatusCode: resp.StatusCode,

			UpstreamRequestID: resp.Header.Get("x-request-id"),

			Kind: "failover",

			Message: message,

			Detail: s.getUpstreamErrorDetail(body),
		}
		if resp.StatusCode == http.StatusUnauthorized {
			event.Stage = string(protocolforward.GatewayFailureStageProviderAuth)
			event.Scope = string(protocolforward.GatewayFailureScopeProvider)
			event.Reason = string(protocolforward.AntigravityCredentialRejectedReason)
			c.Observe(event)
			return antigravityCredentialRejectedError(resp, body)
		}
		c.Observe(event)
		return &protocolforward.UpstreamFailoverError{
			StatusCode: resp.StatusCode,

			ResponseBody: body,

			ResponseHeaders: resp.Header.Clone(),
		}
	}
	return c.MappedAntigravityCompatError(provider, resp.StatusCode, resp.Header.Get("x-request-id"), body)
}

func antigravityCredentialRejectedError(resp *http.Response, body []byte) *protocolforward.UpstreamFailoverError {
	return &protocolforward.UpstreamFailoverError{
		StatusCode: resp.StatusCode,

		ResponseBody: body,

		ResponseHeaders: resp.Header.Clone(),

		Stage: protocolforward.GatewayFailureStageProviderAuth,

		Scope: protocolforward.GatewayFailureScopeProvider,

		Reason:             protocolforward.AntigravityCredentialRejectedReason,
		NextProviderAction: protocolforward.NextProviderRetry,
		ClientStatusCode:   http.StatusBadGateway,

		ClientMessage: protocolforward.AntigravityCredentialRejectedClientMessage,
	}
}
