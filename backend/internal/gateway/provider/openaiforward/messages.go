package openaiforward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func RunMessages(ctx context.Context, body []byte, promptCacheKey, defaultMappedModel string, p MessagesPorts) (*Result, error) {
	profile, route, err := p.Prepare(ctx)
	if err != nil {
		return nil, err
	}
	if route != DispatchResponses {
		return p.Dispatch(ctx, route, body, promptCacheKey, defaultMappedModel)
	}
	startTime := time.Now()

	var anthropicReq protocolanthropic.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	if err := p.ValidateEffort(body, anthropicReq.Model); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	anthropicDigestReq := p.CloneDigest(&anthropicReq)
	originalModel := anthropicReq.Model
	normalizedModel := anthropicReq.Model
	clientStream := anthropicReq.Stream // client's original stream preference

	billingModel := p.BillingModel(normalizedModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	anthropicReq.Model = upstreamModel
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	apiKeyID := p.APIKeyID()
	anthropicDigestChain := ""
	anthropicMatchedDigestChain := ""
	compatPromptCacheInjected := false
	// Use Claude Code's stable session identity as Grok's cache seed when available.
	// Grok skips the GPT/Codex key injector, so this lets successive Messages turns
	// reuse the xAI cache identity.
	if promptCacheKey == "" && profile.Platform == "grok" {
		if sessionSeed := p.ClaudeSession(body); sessionSeed != "" {
			promptCacheKey = sessionSeed
			compatPromptCacheInjected = true
		} else if sessionSeed := p.MetadataSession(&anthropicReq); sessionSeed != "" {
			promptCacheKey = sessionSeed
			compatPromptCacheInjected = true
		}
	}
	if promptCacheKey == "" && p.AutoCacheKey(upstreamModel) {
		promptCacheKey = p.MetadataSession(&anthropicReq)
		if promptCacheKey == "" {
			promptCacheKey = p.CacheControlKey(&anthropicReq)
		}
		if promptCacheKey == "" {
			anthropicDigestChain = p.DigestChain(anthropicDigestReq)
			if reusedKey, matchedChain := p.FindDigestKey(apiKeyID, anthropicDigestChain); reusedKey != "" {
				promptCacheKey = reusedKey
				anthropicMatchedDigestChain = matchedChain
			} else {
				promptCacheKey = p.DigestKey(anthropicDigestChain)
			}
		}
		compatPromptCacheInjected = promptCacheKey != ""
	}
	compatReplayTrimmed := false
	compatReplayGuardEnabled := p.AutoCacheKey(upstreamModel)
	compatContinuationEnabled := p.ContinuationEnabled(upstreamModel)
	previousResponseID := ""
	if compatContinuationEnabled {
		previousResponseID = p.ResponseID(ctx, promptCacheKey)
	}
	compatContinuationDisabled := compatContinuationEnabled &&
		p.ContinuationDisabled(ctx, promptCacheKey)
	compatTurnState := ""
	// Codex uses session_id and x-codex-turn-state for caching. Keep the full replay
	// so the cached prefix can grow beyond system instructions and tools between turns.
	if compatReplayGuardEnabled && !profile.UsesCodex && previousResponseID == "" && !compatContinuationDisabled {
		compatReplayTrimmed = p.ReplayGuard(&anthropicReq)
	}

	// Convert after applying the compatibility replay guard.
	responsesReq, err := p.Convert(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic to responses: %w", err)
	}

	// Force upstream streaming for endpoints that require it; client preference controls the response format.
	responsesReq.Stream = true
	isStream := true

	// Map the Anthropic fast-mode beta to the Responses priority tier.
	if p.BetaFast() {
		responsesReq.ServiceTier = "priority"
	}

	responsesReq.Model = upstreamModel
	if responsesReq.Reasoning != nil {
		responsesReq.Reasoning.Effort = p.MessagesEffort(&anthropicReq, upstreamModel, responsesReq.Reasoning.Effort)
	}
	if previousResponseID != "" {
		responsesReq.PreviousResponseID = previousResponseID
		p.TrimLatestTurn(responsesReq)
	}
	if compatReplayGuardEnabled && !profile.UsesCodex {
		p.TodoGuard(responsesReq)
	}

	logFields := []zap.Field{
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("normalized_model", normalizedModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", isStream),
		zap.Bool("compat_continuation_supported", profile.ContinuationSupported),
	}
	if compatPromptCacheInjected {
		logFields = append(logFields,
			zap.Bool("compat_prompt_cache_key_injected", true),
			zap.String("compat_prompt_cache_key_sha256", p.HashForLog(promptCacheKey)),
		)
	}
	if compatReplayTrimmed {
		logFields = append(logFields,
			zap.Bool("compat_full_replay_trimmed", true),
			zap.Int("compat_messages_after_trim", len(anthropicReq.Messages)),
		)
	}
	if previousResponseID != "" {
		logFields = append(logFields,
			zap.Bool("compat_previous_response_id_attached", true),
			zap.String("compat_previous_response_id", p.Truncate(previousResponseID, p.LogIDLimit())),
		)
	}
	if compatTurnState != "" {
		logFields = append(logFields, zap.Bool("compat_turn_state_attached", true))
	}
	p.Debug("openai messages: model mapping applied", logFields...)

	responsesBody, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}

	if profile.UsesCodex && profile.Platform != "grok" {
		var reqBody map[string]any
		if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
			return nil, fmt.Errorf("unmarshal for codex transform: %w", err)
		}
		codexResult := p.CodexTransform(reqBody, openai.CodexOAuthTransformOptions{
			SkipDefaultInstructions: true,
			PreserveToolCallIDs:     true,
		})
		if codexResult.Error != nil {
			p.Error(http.StatusBadRequest, "invalid_request_error", codexResult.Error.Error())
			return nil, codexResult.Error
		}
		p.ToolNameReverse(codexResult.ToolNameReverse)
		forcedTemplateText := p.ForcedTemplate()
		templateUpstreamModel := upstreamModel
		if codexResult.NormalizedModel != "" {
			templateUpstreamModel = codexResult.NormalizedModel
		}
		existingInstructions, _ := reqBody["instructions"].(string)
		if strings.TrimSpace(existingInstructions) == "" {
			existingInstructions = openai.ExtractPromptLikeInstructionsFromInput(reqBody)
		}
		if _, err := p.ForcedInstructions(reqBody, forcedTemplateText, TemplateData{
			ExistingInstructions: strings.TrimSpace(existingInstructions),
			OriginalModel:        originalModel,
			NormalizedModel:      normalizedModel,
			BillingModel:         billingModel,
			UpstreamModel:        templateUpstreamModel,
		}); err != nil {
			return nil, err
		}
		p.EnsureInstructions(reqBody)
		if p.AutoCacheKey(upstreamModel) {
			p.TodoGuardBody(reqBody)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
		p.ProviderIdentity(reqBody, apiKeyID)
		delete(reqBody, "prompt_cache_key")
		if p.AutoCacheKey(upstreamModel) {
			compatTurnState = p.TurnState(ctx, promptCacheKey)
		}
		// The Codex transform forces stream=true, so read its response as a stream.
		isStream = true
		responsesBody, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("remarshal after codex transform: %w", err)
		}
	}

	// API key providers receive prompt_cache_key in the body, sharing the same
	// upstream cache identity convention as Responses clients.
	if profile.Type == "apikey" {
		if trimmedKey := strings.TrimSpace(promptCacheKey); trimmedKey != "" {
			var reqBody map[string]any
			if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
				return nil, fmt.Errorf("unmarshal for prompt cache key injection: %w", err)
			}
			if existing, ok := reqBody["prompt_cache_key"].(string); !ok || strings.TrimSpace(existing) == "" {
				reqBody["prompt_cache_key"] = trimmedKey
				updated, err := json.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("remarshal after prompt cache key injection: %w", err)
				}
				responsesBody = updated
			}
		}
	}
	// Apply the policy bound to client output_config.effort after model and prompt rewriting.
	if profile.Platform == "openai" {
		policyBody, changed, policyErr := p.ApplyEffort(ctx, responsesBody)
		if policyErr != nil {
			return nil, policyErr
		}
		if changed {
			responsesBody = policyBody
			if responsesReq.Reasoning != nil {
				responsesReq.Reasoning.Effort = gjson.GetBytes(responsesBody, "reasoning.effort").String()
			}
		}
	}

	// Apply fast-mode policy to the outgoing tier, including the Claude beta filter.
	updatedBody, policyErr := p.ApplyFast(ctx, upstreamModel, responsesBody)
	if policyErr != nil {
		return nil, policyErr
	}
	responsesBody = updatedBody
	if serviceTier := p.ServiceTier(responsesBody); serviceTier != nil {
		responsesReq.ServiceTier = *serviceTier
	} else {
		responsesReq.ServiceTier = ""
	}
	grokCacheIdentity := ""
	if profile.Platform == "grok" {
		grokIntentBody := responsesBody
		grokCacheIdentity = p.GrokCacheIdentity(grokIntentBody, promptCacheKey, upstreamModel)
		patchedBody, patchErr := p.PatchGrokBody(grokIntentBody, upstreamModel)
		if patchErr != nil {
			return nil, patchErr
		}
		responsesBody, patchErr = p.ApplyGrokCache(patchedBody, grokIntentBody, grokCacheIdentity, profile.GrokOAuth)
		if patchErr != nil {
			return nil, fmt.Errorf("apply grok prompt cache identity: %w", patchErr)
		}
		responsesBody, patchErr = p.GrokFreeToolRoute(responsesBody, grokIntentBody, grokCacheIdentity)
		if patchErr != nil {
			return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", patchErr)
		}
	}

	token, err := p.Credential(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	if profile.UsesCodex && profile.Platform != "grok" {
		// Select Messages bridge construction even when the body lacks todo-guard or cache-key
		// markers, such as after mapping to another model family. This controls body and session handling.
		// Restore identity headers after construction.
		p.BindMessagesBridge(true)
	}
	upstreamCtx, releaseUpstreamCtx := p.UpstreamContext(ctx)
	upstreamReq, err := p.Build(ctx, upstreamCtx, responsesBody, token, isStream, promptCacheKey, grokCacheIdentity)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	// Derive a deterministic session UUID from the isolated key to separate API key sessions.
	if profile.Platform != "grok" && promptCacheKey != "" {
		isolatedSessionID := p.IsolatedSessionID(apiKeyID, promptCacheKey)
		upstreamReq.Header.Set("session_id", isolatedSessionID)
		if upstreamReq.Header.Get("conversation_id") != "" {
			upstreamReq.Header.Set("conversation_id", isolatedSessionID)
		}
	}
	if profile.UsesCodex && profile.Platform != "grok" {
		// Bridge construction removes identity headers. Restore originator and OpenAI-Beta
		// before sending; the Codex upstream can return 404 when they are missing (issue #3901).
		p.RestoreIdentity(upstreamReq.Header)
		p.Debug("openai messages: upstream identity restored",
			zap.Int64("provider_id", profile.ID),
			zap.String("upstream_model", upstreamModel),
			zap.Bool("compat_identity_restored", true),
		)
	}
	if profile.UsesCodex && promptCacheKey != "" && strings.TrimSpace(p.Header("conversation_id")) == "" {
		upstreamReq.Header.Del("conversation_id")
	}
	if compatTurnState != "" && upstreamReq.Header.Get("x-codex-turn-state") == "" {
		upstreamReq.Header.Set("x-codex-turn-state", compatTurnState)
	}

	// Prepare transport once before the send loop.
	p.PrepareTransport()
	// Grok can reject encrypted reasoning replayed under another OAuth provider or cache identity.
	// Remove that ciphertext and retry once before treating the 400 as terminal or requesting failover.
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if profile.Platform != "grok" {
				break
			}
			upstreamCtxRetry, releaseRetry := p.UpstreamContext(ctx)
			upstreamReq, err = p.Build(ctx, upstreamCtxRetry, responsesBody, token, isStream, promptCacheKey, grokCacheIdentity)
			releaseRetry()
			if err != nil {
				return nil, fmt.Errorf("build grok retry request: %w", err)
			}
		}
		resp, err = p.Send(upstreamReq)
		if err != nil {
			return nil, p.TransportError(ctx, err)
		}
		if profile.Platform != "grok" || attempt > 0 || resp.StatusCode != http.StatusBadRequest {
			break
		}
		respBody := p.ReadErrorBody(resp)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		// Recognize decryption errors first. An opaque 400 also permits one repair attempt
		// when the outbound body still contains encrypted reasoning.
		shouldStrip := p.GrokInvalidEncrypted(resp.StatusCode, respBody) ||
			p.GrokHasEncrypted(responsesBody)
		if !shouldStrip {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		retryBody, changed, trimErr := p.TrimGrokEncrypted(responsesBody)
		if trimErr != nil {
			return nil, fmt.Errorf("prepare Grok invalid encrypted_content retry: %w", trimErr)
		}
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		responsesBody = retryBody
		p.Info("openai messages: retrying after stripping invalid Grok encrypted_content",
			zap.Int64("provider_id", profile.ID),
			zap.Bool("cache_identity_present", strings.TrimSpace(grokCacheIdentity) != ""),
			zap.String("upstream_error_preview", p.Truncate(string(respBody), 240)),
		)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := p.ReadUpstreamError(resp)
		if !p.AgentRecoveryTried(ctx) && p.IsAgentIdentity(ctx) && p.InvalidAgentTask(resp.StatusCode, respBody) {
			if err := p.RecoverAgentTask(ctx); err != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", err)
			}
			return RunMessages(p.MarkAgentRecovery(ctx), body, promptCacheKey, defaultMappedModel, p)
		}
		respBody = p.RedactErrorBody(ctx, respBody)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		upstreamMsg := p.ErrorMessage(respBody)
		if previousResponseID != "" && (p.PreviousMissing(resp.StatusCode, upstreamMsg, respBody) || p.PreviousUnsupported(resp.StatusCode, upstreamMsg, respBody)) {
			if p.PreviousUnsupported(resp.StatusCode, upstreamMsg, respBody) {
				p.DisableContinuation(ctx, promptCacheKey)
			} else {
				p.DeleteResponseID(ctx, promptCacheKey)
			}
			p.Info("openai messages: previous_response_id unavailable, retrying without continuation",
				zap.Int64("provider_id", profile.ID),
				zap.String("previous_response_id", p.Truncate(previousResponseID, p.LogIDLimit())),
				zap.String("upstream_model", upstreamModel),
				zap.Bool("compat_continuation_supported", profile.ContinuationSupported),
			)
			return RunMessages(ctx, body, promptCacheKey, defaultMappedModel, p)
		}
		// A decryption failure can survive provider switching. Strip client-body thinking
		// signatures once so the next attempt can replay the tool continuation.
		if profile.Platform == "grok" &&
			p.GrokInvalidEncrypted(resp.StatusCode, respBody) &&
			!p.GrokStripRetried(ctx) {
			if strippedBody, ok := p.StripThinkingSignatures(body); ok {
				p.Info("openai messages: stripping thinking signatures for Grok failover retry",
					zap.Int64("provider_id", profile.ID),
				)
				return RunMessages(p.MarkGrokStrip(ctx), strippedBody, promptCacheKey, defaultMappedModel, p)
			}
		}
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		// Format errors that remain after failover checks as Anthropic errors.
		return p.ErrorResponse(resp, billingModel)
	}
	if profile.Platform == "grok" && profile.Type == "oauth" && !profile.Shadow {
		p.UpdateGrokUsage(ctx, upstreamModel, resp.Header, resp.StatusCode)
	}

	if profile.UsesCodex && promptCacheKey != "" {
		if turnState := strings.TrimSpace(resp.Header.Get("x-codex-turn-state")); turnState != "" {
			p.BindTurnState(ctx, promptCacheKey, turnState)
		}
	}

	// Read the upstream stream and format output according to the client's preference.
	var result *Result
	var handleErr error
	var nativeResult *openai.CompatResponseResult
	output := upstream.NewDeferredOutputContext(p.Sink())
	responseOptions := p.ResponseOptions(resp, originalModel, billingModel, upstreamModel)
	if clientStream {
		nativeResult, handleErr = openai.ReadMessagesStreaming(resp, output, responseOptions, originalModel, upstreamModel, startTime)
	} else {
		nativeResult, handleErr = openai.ReadMessagesBuffered(resp, output, responseOptions, originalModel, upstreamModel, startTime)
	}
	result = FromCompatResult(nativeResult, billingModel)
	if p.CyberPolicy() {
		if handleErr == nil {
			handleErr = p.CyberError()
		}
		return nil, handleErr
	}

	// Save continuation state and the final billing controls after successful output.
	if handleErr == nil && result != nil {
		if compatContinuationEnabled && promptCacheKey != "" && result.ResponseID != "" {
			p.BindResponseID(ctx, promptCacheKey, result.ResponseID)
		}
		if promptCacheKey != "" && anthropicDigestChain != "" {
			p.BindDigestKey(apiKeyID, anthropicDigestChain, promptCacheKey, anthropicMatchedDigestChain)
		}
		// Prefer the upstream's echoed tier; otherwise use the outgoing body's tier
		// after fast-policy filtering or forcing.
		if tier := p.ResolvedServiceTier(p.ServiceTier(responsesBody)); tier != nil {
			result.ServiceTier = tier
		}
		if responsesReq.Reasoning != nil && responsesReq.Reasoning.Effort != "" {
			re := responsesReq.Reasoning.Effort
			result.ReasoningEffort = &re
		}
	}

	// Update OAuth Codex usage from response headers.
	// Spark shadows obtain their codex_* snapshot through QueryUsage's bengalfox query.
	if handleErr == nil && profile.Type == "oauth" && !profile.Shadow && profile.Platform != "grok" {
		p.UpdateCodexUsage(ctx, resp.Header)
	}

	return result, handleErr
}
