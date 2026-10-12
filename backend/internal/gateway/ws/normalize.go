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

// Normalize 逐轮保持客户端模型、提供商身份、图片与 Fast 策略的原执行顺序。
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
		// 后续 response.create 帧先执行用户提示词替换，再进入模型归一化、图片桥接和 OpenAI Fast Policy。
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
		// 客户端可能仅在首轮 response.create 指定 model，后续省略时复用上一轮已校验的客户端模型。
		// 随后把映射结果写入上游 payload，模型映射、Fast 策略和图片权限使用同一模型。
		originalModel = s.State.OriginalModel
		if originalModel == "" {
			return ClientPayload{}, p.CloseError(
				1008,
				"model is required in response.create payload",
				nil,
			)
		}
	}
	// 每轮先解析分组映射，再解析提供商映射，originalModel 保存客户端请求的模型。
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
	// 生图能力必须按分组映射模型 G 判断；提供商最终模型 U 只用于真正的上游请求。
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

	// 所有 WebSocket turn 在此应用与 HTTP 相同的 Fast 评估、规范化和适用范围规则。
	// 首轮需要 model，后续省略时使用 s.State.OriginalModel。策略评估前写入上游模型，供白名单和 filter 使用。
	policyCtx := ctx
	policyApplied, blocked, policyErr := p.FastPolicy(policyCtx, turn, upstreamModel, normalized, true)
	if policyErr != nil {
		return ClientPayload{}, p.CloseError(1008, "invalid websocket request payload", policyErr)
	}
	if blocked != nil {
		p.PolicyDenied()
		// Send a Realtime-style error event to the client first, then
		// signal the handler to close the connection with PolicyViolation.
		// We intentionally do NOT forward this frame upstream.
		//
		// coder/websocket@v1.8.14 Conn.Write is synchronous and flushes
		// the underlying bufio writer before returning (write.go:42 →
		// 307-311), and the subsequent close handshake re-acquires the
		// same writeFrameMu, so the error event is guaranteed to reach
		// the kernel send buffer before any close frame is queued.
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

// ApplyReasoningEffortPolicy 将同一套分组策略应用到 WS 请求帧。
// requestModel 由调用方提供客户端模型，支持后续省略 model 的多轮帧。
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
