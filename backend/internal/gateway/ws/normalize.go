package ws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Normalize validates a turn and applies model, identity, image, and Fast policies in forwarding order.
func (s *RequestNormalizer) Normalize(ctx context.Context, raw []byte, applyUserPromptReplacement bool, turn int) (ClientPayload, error) {
	p, o := s.Port, s.Options
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ClientPayload{}, p.CloseError(1008, "empty websocket request payload", nil)
	}
	if !gjson.ValidBytes(trimmed) {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", errors.New("invalid json"))
	}
	if applyUserPromptReplacement {
		// Replace prompts before model normalization, image bridging, and Fast policy.
		trimmed = p.PromptReplace(ctx, trimmed)
	}

	values := gjson.GetManyBytes(trimmed, "type", "model", "prompt_cache_key", "previous_response_id")
	eventType := strings.TrimSpace(values[0].String())
	normalized := trimmed
	switch eventType {
	case "":
		eventType = "response.create"
		next, setErr := p.Mutate(normalized, "type", eventType)
		if setErr != nil {
			return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", setErr)
		}
		normalized = next
	case "response.create":
	case "response.append":
		return ClientPayload{}, p.CloseError(
			1008,
			"response.append is not supported in ws v2; use response.create with previous_response_id",
			nil,
		)
	default:
		return ClientPayload{}, p.CloseError(
			1008,
			fmt.Sprintf("unsupported websocket request type: %s", eventType),
			nil,
		)
	}
	policyRequestModel := strings.TrimSpace(values[1].String())
	if policyRequestModel == "" {
		policyRequestModel = s.State.OriginalModel
	}
	requestedReasoningEffort := p.RequestedEffort(normalized, strings.TrimSpace(values[1].String()))
	next, policyErr := p.Reasoning(normalized, policyRequestModel)
	if policyErr != nil {
		return ClientPayload{}, p.CloseError(1008, policyErr.Error(), policyErr)
	}
	normalized = next
	responsesLite := p.IsLite(normalized)
	if compatibilityBody, compatibilityChanged, compatibilityErr := p.Compatibility(normalized, responsesLite); compatibilityErr != nil {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", compatibilityErr)
	} else if compatibilityChanged {
		normalized = compatibilityBody
	}
	if o.OAuth && !o.ForceHTTPBridge {
		aliasedBody, aliasErr := p.AliasTools(normalized)
		if aliasErr != nil {
			return ClientPayload{}, p.CloseError(1008, aliasErr.Error(), aliasErr)
		}
		normalized = aliasedBody
	}

	originalModel := strings.TrimSpace(values[1].String())
	modelMissing := originalModel == ""
	if originalModel == "" {
		// When later frames omit model, reuse the validated client model from session state.
		// Resolve its mappings before applying model-dependent policies and writing the upstream payload.
		originalModel = s.State.OriginalModel
		if originalModel == "" {
			return ClientPayload{}, p.CloseError(
				1008,
				"model is required in response.create payload",
				nil,
			)
		}
	}
	// Resolve group mapping before provider mapping; retain the client model in originalModel.
	routingModel, upstreamModel, resolveModelErr := p.Models(turn, originalModel, normalized)
	if resolveModelErr != nil {
		return ClientPayload{}, resolveModelErr
	}
	normalized, resolveModelErr = p.ModelAttributes(ctx, turn, upstreamModel, normalized)
	if resolveModelErr != nil {
		return ClientPayload{}, resolveModelErr
	}
	promptCacheKey := strings.TrimSpace(values[2].String())
	previousResponseID := strings.TrimSpace(values[3].String())
	previousResponseIDKind := p.ClassifyPrevious(previousResponseID)
	if previousResponseID != "" && previousResponseIDKind == "message_id" {
		return ClientPayload{}, p.CloseError(
			1008,
			"previous_response_id must be a response.id (resp_*), not a message id",
			nil,
		)
	}
	if turnMetadata := p.TurnMetadata(); turnMetadata != "" {
		next, setErr := p.Mutate(normalized, "client_metadata.x-codex-turn-metadata", turnMetadata)
		if setErr != nil {
			return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", setErr)
		}
		normalized = next
	}
	providerIdentitySourceRaw := append([]byte(nil), normalized...)
	providerScopedPayload, providerScoped, scopeErr := p.ScopeIdentity(normalized)
	if scopeErr != nil {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket identity metadata", scopeErr)
	}
	if providerScoped {
		normalized = providerScopedPayload
	}
	if p.IsLite(normalized) {
		litePayload, liteErr := p.NormalizeLite(normalized)
		if liteErr != nil {
			return ClientPayload{}, p.CloseError(
				1008,
				liteErr.Error(),
				liteErr,
			)
		}
		normalized = litePayload
	}
	imagePolicy := p.ImagePolicy(ctx, normalized)
	imageGenerationAllowed := imagePolicy.Allowed
	codexImageGenerationExplicitToolPolicy := imagePolicy.Explicit
	codexBridgeEnabled := imagePolicy.Bridge
	if codexBridgeEnabled {
		next, err := p.BridgeImages(normalized)
		if err != nil {
			return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", err)
		}
		normalized = next
	}

	if modelMissing || upstreamModel != originalModel {
		next, setErr := p.Mutate(normalized, "model", upstreamModel)
		if setErr != nil {
			return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", setErr)
		}
		normalized = next
	}
	if codexImageGenerationExplicitToolPolicy == "strip" {
		if stripped, changed, stripErr := p.StripImages(normalized); stripErr != nil {
			return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", stripErr)
		} else if changed {
			normalized = stripped
			p.Log(fmt.Sprintf("ingress_ws_codex_image_tool_stripped_by_policy provider_id=%d", o.ProviderID))
		}
	}
	if stripped, changed, stripErr := p.StripSparkImages(normalized, upstreamModel); stripErr != nil {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", stripErr)
	} else if changed {
		normalized = stripped
		p.Log(fmt.Sprintf("ingress_ws_codex_spark_image_tool_stripped provider_id=%d", o.ProviderID))
	}
	// Check image eligibility against group model G; use final model U for the upstream request.
	imageIntentBody, imageIntent, explicitImageIntent := p.ImageIntent(routingModel, upstreamModel, normalized)
	if explicitImageIntent && !imageGenerationAllowed {
		p.FeatureDenied()
		return ClientPayload{}, p.CloseError(1008, p.ImageDeniedMessage(), nil)
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfgErr error
		imageCfg, imageCfgErr := p.ImageBilling(imageIntentBody, routingModel)
		if imageCfgErr != nil {
			return ClientPayload{}, p.CloseError(1008, imageCfgErr.Error(), imageCfgErr)
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}

	// Apply the HTTP Fast policy rules to each WebSocket turn.
	// The resolved upstream model drives whitelist matching, including when the frame omits model.
	policyCtx := ctx
	policyApplied, blocked, policyErr := p.FastPolicy(policyCtx, turn, upstreamModel, normalized, true)
	if policyErr != nil {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", policyErr)
	}
	if blocked != nil {
		p.PolicyDenied()
		// Attempt the policy error event before returning the close instruction.
		// A successful synchronous write orders the event before the close frame.
		// The blocked request is not forwarded upstream.
		eventBytes := p.BlockedEvent(blocked)
		if eventBytes != nil {
			p.WriteBlocked(ctx, eventBytes)
		}
		return ClientPayload{}, p.CloseError(
			1008,
			blocked.Message,
			blocked,
		)
	}
	normalized = policyApplied
	s.State.OriginalModel = originalModel

	return ClientPayload{
		PayloadRaw:                normalized,
		ProviderIdentitySourceRaw: providerIdentitySourceRaw,
		RawForHash:                trimmed,
		PromptCacheKey:            promptCacheKey,
		PreviousResponseID:        previousResponseID,
		OriginalModel:             originalModel,
		RoutingModel:              routingModel,
		ImageBillingModel:         imageBillingModel,
		ImageSizeTier:             imageSizeTier,
		ImageInputSize:            imageInputSize,
		PayloadBytes:              len(normalized),
		RequestedReasoningEffort:  requestedReasoningEffort,
	}, nil
}

// ApplyReasoningEffortPolicy applies group reasoning rules to a WebSocket request frame.
// The caller supplies the client requestModel for later frames that omit model.
func ApplyReasoningEffortPolicy(payload []byte, hooks *OpenAIIngressHooks, requestModel string) ([]byte, error) {
	if hooks == nil || (hooks.MaxReasoningEffort == "" && len(hooks.ReasoningEffortMappings) == 0) {
		return payload, nil
	}
	updated, changed, err := requeststate.ApplyOpenAIReasoningEffortPolicyForModel(
		payload,
		hooks.MaxReasoningEffort,
		hooks.ReasoningEffortMappings,
		hooks.MaxReasoningEffortOverLimit,
		strings.TrimSpace(requestModel),
	)
	if err != nil {
		return payload, err
	}
	if changed {
		return updated, nil
	}
	return payload, nil
}
