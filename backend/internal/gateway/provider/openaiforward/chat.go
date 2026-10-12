package openaiforward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// CursorResponsesUnsupportedFields lists fields removed from Responses-shaped Chat requests.
var CursorResponsesUnsupportedFields = []string{"prompt_cache_retention", "safety_identifier", "metadata", "stream_options"}

func RunChat(ctx context.Context, body []byte, promptCacheKey, defaultMappedModel string, compatPromptCacheTenantIsolated bool, p ChatPorts) (*Result, error) {
	profile, err := p.PrepareChat(ctx)
	if err != nil {
		return nil, err
	}
	if !p.ClientAllowed(ctx, body) {
		p.PolicyDenied()
		p.Reject(403, "forbidden_error", "This provider only allows Codex official clients")
		return nil, errors.New("codex_cli_only restriction: only codex official clients are allowed")
	}
	if profile.Protocol != "" && profile.Protocol != protocol.ProtocolOpenAIResponses && !gjson.GetBytes(body, "messages").Exists() && gjson.GetBytes(body, "input").Exists() {
		var request protocolopenai.ResponsesRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		converted, err := p.ResponsesToChat(&request)
		if err != nil {
			return nil, err
		}
		body, err = json.Marshal(converted)
		if err != nil {
			return nil, err
		}
	}
	if profile.Platform == "grok" {
		if profile.Protocol == protocol.ProtocolOpenAIChatCompletions {
			return p.DispatchChat(ctx, DispatchRawChat, body, promptCacheKey, defaultMappedModel)
		}
		if profile.Protocol == protocol.ProtocolOpenAIResponses {
			if eligible, reason := p.GrokBridgeEligible(body); !eligible {
				return nil, fmt.Errorf("configured Grok Responses conversion cannot preserve request: %s", reason)
			}
			return p.DispatchChat(ctx, DispatchGrok, body, promptCacheKey, defaultMappedModel)
		}
		if profile.GrokOAuth {
			eligible, reason := p.GrokBridgeEligible(body)
			if eligible {
				return p.DispatchChat(ctx, DispatchGrok, body, promptCacheKey, defaultMappedModel)
			}
			p.Debug("grok chat_completions: using raw fallback",
				zap.Int64("provider_id", profile.ID),
				zap.String("reason", reason),
			)
		}
		return p.DispatchChat(ctx, DispatchRawChat, body, promptCacheKey, defaultMappedModel)
	}
	if err := p.ValidateEffort(body, gjson.GetBytes(body, "model").String()); err != nil {
		p.ChatError(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}

	// Detect Responses-shaped bodies before adaptive dispatch so input is converted
	// before reaching an upstream that expects messages.
	isResponsesShape := !gjson.GetBytes(body, "messages").Exists() && gjson.GetBytes(body, "input").Exists()

	// Adaptive providers send standard Chat bodies to Chat endpoints. Responses-shaped
	// bodies use native Responses on DeepSeek/Kimi and convert to Chat on Zhipu.
	if profile.Adaptive {
		if !isResponsesShape {
			return p.DispatchChat(ctx, DispatchRawChat, body, promptCacheKey, defaultMappedModel)
		}
		if !profile.SupportsNativeCN {
			var responsesReq protocolopenai.ResponsesRequest
			if err := json.Unmarshal(body, &responsesReq); err != nil {
				return nil, fmt.Errorf("parse responses-shaped chat completions request: %w", err)
			}
			chatReq, err := p.ResponsesToChat(&responsesReq)
			if err != nil {
				return nil, fmt.Errorf("convert responses-shaped chat completions request: %w", err)
			}
			chatBody, err := json.Marshal(chatReq)
			if err != nil {
				return nil, fmt.Errorf("marshal converted chat completions request: %w", err)
			}
			return p.DispatchChat(ctx, DispatchRawChat, chatBody, promptCacheKey, defaultMappedModel)
		}
		// Native CN Responses requests continue to the Responses-to-Chat output conversion below.
	}

	if profile.Anthropic {
		return p.DispatchChat(ctx, DispatchAnthropic, body, promptCacheKey, defaultMappedModel)
	}

	// RawChat includes CN providers fixed to Chat and API key providers whose resolved
	// probe or administrator policy selects Chat.
	if profile.RawChat {
		return p.DispatchChat(ctx, DispatchRawChat, body, promptCacheKey, defaultMappedModel)
	}
	if !profile.CNProvider && p.DefaultChat() {
		return p.DispatchChat(ctx, DispatchRawChat, body, promptCacheKey, defaultMappedModel)
	}

	startTime := time.Now()

	var chatReq protocolopenai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := chatReq.Model
	clientStream := chatReq.Stream

	// Resolve the final model before deriving a stable compatibility cache seed.
	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	chatReq.Model = upstreamModel

	promptCacheKey = strings.TrimSpace(promptCacheKey)
	compatPromptCacheInjected := false
	if promptCacheKey == "" && !isResponsesShape && (profile.UsesCodex || profile.OpenAIAPIKey) && p.AutoCacheKey(upstreamModel) {
		promptCacheKey = p.DeriveCacheKey(&chatReq, upstreamModel)
		compatPromptCacheInjected = promptCacheKey != ""
		if compatPromptCacheInjected && profile.OpenAIAPIKey {
			promptCacheKey = p.IsolateCacheKey(promptCacheKey)
			compatPromptCacheTenantIsolated = true
		}
	}

	// Cursor can send a Responses input to the Chat URL. Decoding it through a Chat
	// request would discard input, so patch the raw body before OAuth normalization.
	var (
		responsesReq  *protocolopenai.ResponsesRequest
		responsesBody []byte
	)
	if isResponsesShape {
		responsesBody, err = sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("rewrite model in responses-shape body: %w", err)
		}
		// Raw body patching retains unknown fields, so remove unsupported Codex fields here.
		for _, field := range CursorResponsesUnsupportedFields {
			if stripped, derr := sjson.DeleteBytes(responsesBody, field); derr == nil {
				responsesBody = stripped
			}
		}
		var normalizedServiceTier string
		responsesBody, normalizedServiceTier, err = p.NormalizeBodyTier(responsesBody)
		if err != nil {
			return nil, fmt.Errorf("normalize service_tier in responses-shape body: %w", err)
		}
		// Save the normalized tier for completion processing.
		responsesReq = &protocolopenai.ResponsesRequest{
			Model:       upstreamModel,
			ServiceTier: normalizedServiceTier,
		}
	} else {
		// The Chat-to-Responses converter requests upstream streaming.
		responsesReq, err = p.ChatToResponses(&chatReq)
		if err != nil {
			return nil, fmt.Errorf("convert chat completions to responses: %w", err)
		}
		responsesReq.Model = upstreamModel
		p.NormalizeRequestTier(responsesReq)
		responsesBody, err = json.Marshal(responsesReq)
		if err != nil {
			return nil, fmt.Errorf("marshal responses request: %w", err)
		}
	}

	logFields := []zap.Field{
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
		zap.Bool("responses_shape", isResponsesShape),
	}
	if compatPromptCacheInjected {
		logFields = append(logFields,
			zap.Bool("compat_prompt_cache_key_injected", true),
			zap.String("compat_prompt_cache_key_sha256", p.HashForLog(promptCacheKey)),
		)
	}
	p.Debug("openai chat_completions: model mapping applied", logFields...)

	if profile.UsesCodex {
		var reqBody map[string]any
		if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
			return nil, fmt.Errorf("unmarshal for codex transform: %w", err)
		}
		isJSONObjectFormat := strings.EqualFold(strings.TrimSpace(gjson.GetBytes(responsesBody, "text.format.type").String()), "json_object")
		codexResult := p.CodexTransform(reqBody, openai.CodexOAuthTransformOptions{
			SkipDefaultInstructions:             !isResponsesShape,
			OmitPromotedSystemMessagesFromInput: !isResponsesShape && !isJSONObjectFormat,
		})
		if codexResult.Error != nil {
			p.Reject(400, "invalid_request_error", codexResult.Error.Error())
			return nil, codexResult.Error
		}
		p.ToolNameReverse(codexResult.ToolNameReverse)
		if !isResponsesShape {
			p.EnsureInstructions(reqBody)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		} else if promptCacheKey != "" {
			reqBody["prompt_cache_key"] = promptCacheKey
		}
		p.ProviderIdentity(reqBody, p.APIKeyID())
		responsesBody, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("remarshal after codex transform: %w", err)
		}
	}
	if profile.Type == "apikey" {
		if trimmedKey := strings.TrimSpace(promptCacheKey); trimmedKey != "" {
			var reqBody map[string]any
			if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
				return nil, fmt.Errorf("unmarshal for prompt cache key injection: %w", err)
			}
			// API key conversion skips the Codex transform, so restore the ingress cache key
			// in the outgoing body when no key is already present.
			if existing, ok := reqBody["prompt_cache_key"].(string); !ok || strings.TrimSpace(existing) == "" {
				reqBody["prompt_cache_key"] = trimmedKey
				responsesBody, err = json.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("remarshal after prompt cache key injection: %w", err)
				}
			}
		}
	}

	// Apply the fast policy before recording the outgoing service tier.
	updatedBody, policyErr := p.ApplyChatFast(ctx, upstreamModel, responsesBody)
	if policyErr != nil {
		return nil, policyErr
	}
	responsesBody = updatedBody
	// Record the outgoing effort so fields discarded during conversion are not
	// reported as forwarded controls in usage records.
	reasoningEffort := p.EffectiveEffort(responsesBody, body, upstreamModel, billingModel, originalModel)
	reasoningEffort = p.ThinkingFallback(reasoningEffort, responsesBody, billingModel)
	if serviceTier := p.ServiceTier(responsesBody); serviceTier != nil {
		responsesReq.ServiceTier = *serviceTier
	} else {
		responsesReq.ServiceTier = ""
	}

	token, err := p.AccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	upstreamCtx, releaseUpstreamCtx := p.UpstreamContext(ctx)
	upstreamReq, err := p.BuildChat(upstreamCtx, responsesBody, token, promptCacheKey)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	if promptCacheKey != "" {
		apiKeyID := p.APIKeyID()
		sessionKey := promptCacheKey
		if !compatPromptCacheTenantIsolated {
			sessionKey = p.UpstreamSessionKey(apiKeyID, promptCacheKey)
		}
		upstreamReq.Header.Set("session_id", p.SessionUUID(sessionKey))
	}

	// Resolve transport settings before sending; retries reuse this preparation.
	p.PrepareTransport()
	resp, err := p.Send(upstreamReq)
	if err != nil {
		return nil, p.TransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := p.ReadUpstreamError(resp)
		if !p.AgentRecoveryTried(ctx) && p.IsAgentIdentity(ctx) && p.InvalidAgentTask(resp.StatusCode, respBody) {
			if err := p.RecoverAgentTask(ctx); err != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", err)
			}
			return RunChat(p.MarkAgentRecovery(ctx), body, promptCacheKey, defaultMappedModel, compatPromptCacheTenantIsolated, p)
		}
		respBody = p.RedactErrorBody(ctx, respBody)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		upstreamMsg := p.ErrorMessage(respBody)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return p.ChatErrorResponse(resp, billingModel)
	}

	var result *Result
	var handleErr error
	var nativeResult *openai.CompatResponseResult
	output := upstream.NewDeferredOutputContext(p.Sink())
	options := p.ChatResponseOptions(resp, originalModel, billingModel, upstreamModel)
	if clientStream {
		nativeResult, handleErr = openai.ReadChatStreaming(resp, output, options, originalModel, upstreamModel, startTime, len(body))
	} else {
		nativeResult, handleErr = openai.ReadChatBuffered(resp, output, options, originalModel, upstreamModel, startTime)
	}
	result = FromCompatResult(nativeResult, billingModel)
	if p.CyberPolicy() {
		if handleErr == nil {
			handleErr = p.CyberError()
		}
		return nil, handleErr
	}

	// Attach the final effort and billing tier to successful results.
	// Prefer the upstream's echoed tier, then the body after fast-policy processing.
	// A filtered tier must not reappear from the original client request.
	if handleErr == nil && result != nil {
		if tier := p.ResolvedServiceTier(p.ServiceTier(responsesBody)); tier != nil {
			result.ServiceTier = tier
		}
		result.ReasoningEffort = reasoningEffort
	}

	// Update Codex usage from response headers for eligible credentials.
	// Spark shadows obtain their codex_* snapshot through QueryUsage's bengalfox query.
	if handleErr == nil && profile.UsesCodex && !profile.Shadow {
		p.UpdateCodexUsage(ctx, resp.Header)
	}

	return result, handleErr
}
