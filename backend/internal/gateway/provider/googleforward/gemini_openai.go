package googleforward

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	protocolforward "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// gemini_openai.go converts OpenAI requests to Gemini and returns
// client-specific responses and completion metadata.
// OpenAI compatibility uses an Anthropic request as an intermediate form before
// preparing Gemini input. Original client bytes remain available for final-
// route reasoning controls and recorded effort, while logical client model and
// mapped upstream model remain separate in response and completion metadata.
//
// Responses tool adaptation retains a restoration mapping for namespaces, and
// Chat carries its include_usage stream choice into the response target.
// Project-bearing OAuth can read an upstream stream into a buffered client
// response. HTTP failure policy runs before response adaptation, using the
// selected client envelope. Successful compatibility image metadata currently
// uses the original client model-name estimate, while native and Messages
// execution consult their own attempt image counter.

// ForwardAsResponses converts one OpenAI Responses body through Anthropic into
// a selected Gemini attempt. It validates input and retains client tool
// adaptation for response restoration.
//
// The original body supplies reasoning controls when final-model attributes are
// applied, so intermediate protocol conversion does not choose those controls
// by itself. Client model and stream mode are retained separately from provider
// mapping and upstream exchange mode. Successful output returns Responses
// presentation and Messages completion metadata through the synchronous HTTP
// adapter.
func (s *Gemini) ForwardAsResponses(
	ctx context.Context,
	output Output,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	_ *requeststate.ParsedRequest,
) (*protocolforward.MessagesResult, error) {
	c := &attempt{Output: output}

	startTime := time.Now()

	adaptedBody, clientToolMapping, err := protocolforward.AdaptResponsesClientToolsForAnthropic(body)
	if err != nil {
		return nil, c.GeminiOpenAICompatError(gemininative.OpenAICompatResponses, http.StatusBadRequest, "invalid_request_error", "Failed to adapt client tools")
	}
	var responsesReq protocolopenai.ResponsesRequest
	if err := json.Unmarshal(adaptedBody, &responsesReq); err != nil {
		return nil, c.GeminiOpenAICompatError(gemininative.OpenAICompatResponses, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(responsesReq.Model) == "" {
		return nil, c.GeminiOpenAICompatError(gemininative.OpenAICompatResponses, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	anthropicReq, err := protocolbridge.ResponsesToAnthropicRequest(&responsesReq)
	if err != nil {
		return nil, c.GeminiOpenAICompatError(gemininative.OpenAICompatResponses, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	anthropicReq.Stream = responsesReq.Stream
	claudeBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal responses compat request: %w", err)
	}

	return s.forwardClaudeBodyAsOpenAICompat(
		ctx,
		c,
		provider,
		claudeBody,
		responsesReq.Model,
		responsesReq.Stream,
		false,
		startTime,
		body,
		gemininative.OpenAICompatResponses,
		clientToolMapping,
	)
}

// ForwardAsChatCompletions converts one Chat request through Responses and
// Anthropic into Gemini input. It retains the original model, client stream
// choice, and requested usage-chunk flag.
//
// Original Chat bytes remain available for reasoning controls after final
// provider model resolution. Project-bearing OAuth can use a streaming upstream
// exchange for a buffered client, while the response target selects Chat
// framing. Completion returns the client model beside the mapped wire model and
// records effort from the original request.
func (s *Gemini) ForwardAsChatCompletions(
	ctx context.Context,
	output Output,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
) (*protocolforward.MessagesResult, error) {
	c := &attempt{Output: output}

	startTime := time.Now()

	var ccReq protocolopenai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		return nil, c.ChatError(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(ccReq.Model) == "" {
		return nil, c.ChatError(http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	originalModel := ccReq.Model
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	responsesReq, err := protocolbridge.ChatCompletionsToResponses(&ccReq, protocolforward.ConversionOptionsForModel(ccReq.Model))
	if err != nil {
		return nil, c.ChatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}

	anthropicReq, err := protocolbridge.ResponsesToAnthropicRequest(responsesReq)
	if err != nil {
		return nil, c.ChatError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	anthropicReq.Stream = clientStream

	claudeBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions compat request: %w", err)
	}

	return s.forwardClaudeBodyAsOpenAICompat(
		ctx,
		c,
		provider,
		claudeBody,
		originalModel,
		clientStream,
		includeUsage,
		startTime,
		body,
		gemininative.OpenAICompatChatCompletions,
		protocolbridge.ResponsesClientToolMapping{},
	)
}

// forwardClaudeBodyAsOpenAICompat prepares and executes the intermediate
// Anthropic body for one selected Gemini provider. Original client bytes supply
// model-attribute controls and recorded reasoning effort; protocol and tool
// mapping select response presentation.
//
// Provider policy classifies HTTP failures before output adaptation, and a
// successful result retains separate client and upstream models. Upstream
// streaming can differ from client mode for project-bearing OAuth. Image
// metadata uses the original client model-name estimate on this path,
// independently of the per-attempt counter used by other Gemini entries.
func (s *Gemini) forwardClaudeBodyAsOpenAICompat(
	ctx context.Context,
	c *attempt,
	provider *gatewayprovider.ExecutionProvider,
	claudeBody []byte,
	originalModel string,
	clientStream bool,
	includeUsage bool,
	startTime time.Time,
	originalBody []byte,
	protocol gemininative.OpenAICompatProtocol,
	clientToolMapping protocolbridge.ResponsesClientToolMapping,
) (*protocolforward.MessagesResult, error) {
	var req struct {
		Model  string `json:"model"`  // converted request model identifier
		Stream bool   `json:"stream"` // converted request stream flag
	}
	if err := json.Unmarshal(claudeBody, &req); err != nil {
		return nil, c.GeminiOpenAICompatError(protocol, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, c.GeminiOpenAICompatError(protocol, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	// Resolve the provider wire model before capability filtering so OpenAI
	// controls from the original body are applied to the same Gemini target
	// sent upstream.
	mappedModel := providercore.ResolveForwardMappedModel(gatewayprovider.ExecutionRecord(provider), req.Model, provideradapter.ModelDefaults())

	geminiReq, err := convertClaudeMessagesToGeminiGenerateContent(claudeBody)
	if err != nil {
		return nil, c.GeminiOpenAICompatError(protocol, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	attributes, err := gatewayprovider.ResolveModelAttributes(ctx, provider, mappedModel, s.Attributes, "gemini_generate_content")
	if err != nil {
		return nil, err
	}
	geminiReq, err = applyGeminiModelParameters(geminiReq, originalBody, attributes, true)
	if err != nil {
		return nil, c.GeminiOpenAICompatError(protocol, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	geminiReq = ensureGeminiFunctionCallThoughtSignatures(geminiReq)

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	useUpstreamStream := clientStream
	if provider.Record.Type == capability.ProviderTypeOAuth && !clientStream && strings.TrimSpace(provider.View().GetCredential("project_id")) != "" {
		useUpstreamStream = true
	}

	buildReq, requestIDHeader := s.buildGeminiChatCompletionsUpstreamRequestFunc(
		provider,
		mappedModel,
		geminiReq,
		clientStream,
		useUpstreamStream,
	)

	options := s.geminiExchangeOptions(c, ctx, provider, mappedModel, geminiExchangeOpenAI, protocol)
	options.Build = buildReq
	options.RequestIDHeader = requestIDHeader
	options.Do = func(req *http.Request) (*http.Response, error) {
		return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
	}
	var requestID string
	var compatibilityResult *protocolforward.MessagesResult
	stopped := false
	var reasoningEffort *string
	clientProtocol := protocolcore.ProtocolOpenAIChatCompletions
	if protocol == gemininative.OpenAICompatResponses {
		clientProtocol = protocolcore.ProtocolOpenAIResponses
	}
	target := &gemininative.Target{
		ProviderID:     provider.Record.ID,
		Model:          mappedModel,
		Mode:           gemininative.OpenAIResponse,
		Exchange:       options,
		Response:       s.geminiResponseAdapter(c).Options,
		StartedAt:      startTime,
		UpstreamStream: useUpstreamStream,
		OAuth:          provider.Record.Type == capability.ProviderTypeOAuth,
		Enter:          s.Enter,
	}
	target.OpenAIProtocol = protocol
	target.IncludeUsage = includeUsage
	target.ClientTools = clientToolMapping
	target.BeforeResponse = func(ctx context.Context, resp *http.Response, requestIDHeader string) (bool, error) {
		var callbackErr error
		compatibilityResult, callbackErr = func() (*protocolforward.MessagesResult, error) {
			requestID = resp.Header.Get(requestIDHeader)
			if requestID == "" {
				requestID = resp.Header.Get("x-goog-request-id")
			}
			if requestID != "" {
				c.Header("x-request-id", requestID)
			}

			if protocol == gemininative.OpenAICompatResponses {
				reasoningEffort = protocolforward.ExtractEffort(originalBody, false, capability.NormalizeRecordedOpenAIEffortForModel, mappedModel)
			} else {
				reasoningEffort = protocolforward.ExtractEffort(originalBody, true, capability.NormalizeRecordedOpenAIEffortForModel, mappedModel)
			}
			// Supply a display effort when a CN model enables
			// thinking without an explicit effort.
			reasoningEffort = gatewayprovider.ApplyThinkingEnabledFallback(reasoningEffort, originalBody, mappedModel)

			if resp.StatusCode >= 400 {
				respBody := s.readUpstreamErrorBody(resp)
				decision := s.applyGeminiUpstreamErrorPolicy(ctx, provider, resp.StatusCode, resp.Header, respBody, mappedModel)
				evBody := gemininative.UnwrapIfNeeded(provider.Record.Type == capability.ProviderTypeOAuth, respBody)
				if decision.Policy == providercore.ErrorPolicyCustomSkipped || decision.Policy == providercore.ErrorPolicyPoolBypassed {
					if failoverErr := s.skippedErrorPolicyFailoverError(c, provider, resp.StatusCode, respBody, requestID); failoverErr != nil {
						return nil, failoverErr
					}
					if decision.Policy == providercore.ErrorPolicyCustomSkipped {
						return nil, c.GeminiCustomCodeSkippedError(provider, resp.StatusCode, requestID, respBody, func() {
							_ = c.ChatError(http.StatusInternalServerError, "api_error", geminiCustomCodeSkippedClientMessage)
						})
					}
					return nil, c.GeminiOpenAICompatMappedError(provider, resp.StatusCode, requestID, evBody, protocol)
				}
				if decision.ShouldReturnGenericError() {
					genericBody := []byte(`{"error":{"message":"Upstream gateway error"}}`)
					return nil, c.GeminiOpenAICompatMappedError(provider, http.StatusInternalServerError, requestID, genericBody, protocol)
				}

				msg400 := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
				googleConfigError := resp.StatusCode == http.StatusBadRequest && upstream.IsGoogleProjectConfigError(msg400)
				if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, googleConfigError || s.shouldFailoverGeminiUpstreamError(resp.StatusCode)) {
					upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(evBody)))
					c.Observe(ops.OpsUpstreamErrorEvent{
						Platform: provider.Record.Platform,

						ProviderID: provider.Record.ID,

						ProviderName: provider.Record.Name,

						UpstreamStatusCode: resp.StatusCode,

						UpstreamRequestID: requestID,

						Kind: "failover",

						Message: upstreamMsg,
					})
					return nil, &protocolforward.UpstreamFailoverError{
						StatusCode: resp.StatusCode,

						ResponseBody: evBody,

						RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
					}
				}

				return nil, c.GeminiOpenAICompatMappedError(provider, resp.StatusCode, requestID, evBody, protocol)
			}

			return nil, nil
		}()
		stopped = resp.StatusCode >= 400 || callbackErr != nil || compatibilityResult != nil
		return stopped, callbackErr
	}
	result, executeErr := (gemininative.Executor{}).Execute(ctx, upstream.AttemptInput{Protocol: clientProtocol, Body: geminiReq, Stream: clientStream, ResponseModel: originalModel, Target: target}, c.Sink())
	if stopped {
		return compatibilityResult, executeErr
	}
	if executeErr != nil {
		return nil, executeErr
	}
	requestID = result.RequestID
	usage := &result.Usage
	firstTokenMs := result.FirstTokenMs

	imageCount := 0
	imageInputSize := s.extractImageInputSize(geminiReq)
	imageSize := media.NormalizeImageSizeTier(imageInputSize)
	// This compatibility result uses the client model name for its
	// single-image estimate; native and Messages paths consult their
	// separate per-attempt image observation counter.
	if antigravity.IsImageGenerationModel(originalModel) {
		imageCount = 1
	}

	return &protocolforward.MessagesResult{
		RequestID: requestID,

		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,

		Usage: *usage,

		Model: originalModel,

		UpstreamModel: mappedModel,

		Stream: clientStream,

		Duration: result.Duration,

		FirstTokenMs: firstTokenMs,

		ReasoningEffort: reasoningEffort,

		ImageCount: imageCount,

		ImageSize: imageSize,

		ImageInputSize: imageInputSize,

		ClientDisconnect: false,
	}, nil
}

// buildGeminiChatCompletionsUpstreamRequestFunc retains the prepared bytes and
// provider plan in a build callback. The caller keeps those inputs usable until
// exchange finishes; token and project callbacks read provider state during
// builds.
func (s *Gemini) buildGeminiChatCompletionsUpstreamRequestFunc(
	provider *gatewayprovider.ExecutionProvider,
	mappedModel string,
	geminiReq []byte,
	clientStream bool,
	useUpstreamStream bool,
) (func(context.Context) (*http.Request, string, error), string) {
	plan := s.geminiRequestPlan(provider, mappedModel, "", false, clientStream, useUpstreamStream, false)
	return func(ctx context.Context) (*http.Request, string, error) {
		return gemininative.BuildRequest(ctx, geminiReq, plan)
	}, "x-request-id"
}
