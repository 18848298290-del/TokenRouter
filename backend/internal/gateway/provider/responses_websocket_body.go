package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// WebSocket Responses input needs the same provider-specific request shapes as
// HTTP preparation, with different cleanup for OAuth and API Key records.
// Compatibility stages can rebuild input or remove historical items, so the
// remote-compaction trigger is placed last only after all earlier input edits
// have completed.
//
// The adapter keeps a temporary body across legacy ingress, reasoning replay,
// input IDs, OAuth controls, orphan outputs, schemas, and image normalization.
// Error paths return the original input and false so a partial prepared body
// is not adopted. Byte editors retain raw values where possible, while orphan
// rebuilding uses UseNumber and image-map decoding uses ordinary JSON
// behavior. The caller applies this transformation before the frame is sent.

// NormalizeResponsesLiteForProvider delegates Lite request cleanup for OpenAI
// targets according to credential type. Nil and other-platform records pass
// through; OAuth uses tools normalization, while other OpenAI types use
// parallel-tool-call normalization.
func NormalizeResponsesLiteForProvider(value *providercore.Record, body []byte) ([]byte, bool, error) {
	if value == nil || value.Platform != capability.PlatformOpenAI {
		return body, false, nil
	}
	if value.IsOpenAIOAuth() {
		return openai.NormalizeResponsesLiteToolsPayload(body)
	}
	return openai.NormalizeResponsesLiteParallelToolCallsPayload(body)
}

// NormalizeOpenAIResponsesWebSocketCompatibilityBody prepares OpenAI Responses
// input through ordered credential-specific compatibility stages. Nil or
// other-platform providers return the supplied body with false, and any stage
// error returns that original body with false.
//
// OAuth-like input first accepts legacy ingress, then all paths normalize
// reasoning replay and item IDs. API Key store=false handling differs from
// OAuth reasoning and internal-field cleanup. OAuth orphan cleanup respects
// previous_response_id, and schema/image rules run before the final
// remote-compaction trigger ordering. changed combines adopted stages. Some
// map-rebuilding stages use ordinary JSON decoding, so callers must not infer
// byte-exact retention of every unrelated number.
func NormalizeOpenAIResponsesWebSocketCompatibilityBody(body []byte, provider *providercore.Record, responsesLite bool) ([]byte, bool, error) {
	if provider == nil || !provider.IsOpenAI() {
		return body, false, nil
	}
	normalized := body
	changed := false
	// OAuth-like endpoints accept the legacy ingress repair before replay cleanup;
	// API Key input retains its native entry shape at this stage.
	if provider.IsOpenAIOAuthLike() {
		var err error
		normalized, changed, err = NormalizeOpenAIResponsesLegacyIngress(body)
		if err != nil {
			return body, false, err
		}
	}
	// Normalize historical reasoning content before credential-specific replay rules
	// so those later rules inspect the same accepted Responses representation.
	if next, normalizedReasoningContent, err := openai.NormalizeOpenAIResponsesReasoningContentReplay(normalized); err != nil {
		return body, false, err
	} else if normalizedReasoningContent {
		normalized = next
		changed = true
	}
	// API Key store=false replay and parallel-call handling are independent of
	// OAuth internal controls, so apply their rules before general input-ID cleanup.
	if provider.IsOpenAIApiKey() {
		if next, normalizedParallel, err := openai.NormalizeOpenAIParallelToolCallsWithoutTools(normalized, responsesLite); err != nil {
			return body, false, err
		} else if normalizedParallel {
			normalized = next
			changed = true
		}
		if next, normalizedReasoning, err := openai.NormalizeOpenAIAPIKeyStoreFalseReasoningReplay(normalized, false); err != nil {
			return body, false, err
		} else if normalizedReasoning {
			normalized = next
			changed = true
		}
	}
	// Invalid item IDs and non-pair call IDs can reject replay while valid call
	// pairing remains needed; sanitize fields before the orphan-output pass considers context.
	if sanitized, idsChanged, err := SanitizeOpenAIResponsesInputItemIDs(normalized); err != nil {
		return body, false, fmt.Errorf("sanitize websocket Responses input item IDs: %w", err)
	} else if idsChanged {
		normalized = sanitized
		changed = true
	}
	// OAuth reasoning mode has an internal endpoint shape. Its normalization follows
	// replay/ID cleanup so it receives the history that will actually be forwarded.
	if provider != nil && provider.IsOpenAI() && provider.IsOAuth() {
		if reasoningBody, reasoningChanged, err := openai.NormalizeOpenAIResponsesReasoningMode(normalized); err != nil {
			return body, false, err
		} else if reasoningChanged {
			normalized = reasoningBody
			changed = true
		}
	}
	if provider != nil && provider.IsOpenAIOAuthLike() {
		oauthBody, oauthChanged, err := openaiprotocol.NormalizeOpenAIOAuthResponsesCompatibilityBody(normalized)
		if err != nil {
			return body, false, err
		}
		normalized = oauthBody
		changed = changed || oauthChanged
		for _, field := range openai.OpenAIChatGPTInternalUnsupportedFields {
			if !gjson.GetBytes(normalized, field).Exists() {
				continue
			}
			next, deleteErr := sjson.DeleteBytes(normalized, field)
			if deleteErr != nil {
				return body, false, fmt.Errorf("normalize websocket body delete %s: %w", field, deleteErr)
			}
			normalized = next
			changed = true
		}
	}
	// OAuth input may contain tool output whose context was removed by earlier
	// cleanup; discard that output only when no previous response can supply its calls.
	needsOrphanCleanup := provider != nil && provider.IsOpenAIOAuthLike() &&
		gjson.GetBytes(normalized, "input").IsArray()
	if needsOrphanCleanup {
		var reqBody map[string]any
		if err := wirejson.DecodeUseNumber(normalized, &reqBody); err != nil {
			return body, false, fmt.Errorf("normalize websocket Responses body: %w", err)
		}
		mapChanged := false
		if needsOrphanCleanup {
			if input, ok := reqBody["input"].([]any); ok && SanitizeOpenAIResponsesOrphanToolOutputs(
				reqBody,
				input,
				strings.TrimSpace(openai.FirstNonEmptyString(reqBody["previous_response_id"])) != "",
			) {
				mapChanged = true
			}
		}
		if mapChanged {
			next, err := wirejson.Marshal(reqBody)
			if err != nil {
				return body, false, fmt.Errorf("serialize normalized websocket Responses body: %w", err)
			}
			normalized = next
			changed = true
		}
	}
	if schemaBody, schemaChanged, err := openai.NormalizeOpenAIResponseFormatSchemasBody(normalized); err != nil {
		return body, false, err
	} else if schemaChanged {
		normalized = schemaBody
		changed = true
	}
	// The image predicate identifies legacy tool fields requiring map normalization.
	// Ordinary JSON decoding here can change unrelated numeric representation when the body is rebuilt.
	if ImageIntent().OpenAIRequestBodyImageGenerationToolNeedsNormalization(normalized) {
		var reqBody map[string]any
		if err := json.Unmarshal(normalized, &reqBody); err != nil {
			return body, false, fmt.Errorf("normalize websocket image tool body: %w", err)
		}
		if openai.NormalizeOpenAIResponsesImageGenerationTools(reqBody) {
			next, err := json.Marshal(reqBody)
			if err != nil {
				return body, false, fmt.Errorf("serialize normalized websocket image tool body: %w", err)
			}
			normalized = next
			changed = true
		}
	}
	if provider != nil {
		if schemaBody, schemaChanged, err := SanitizeOpenAIResponsesToolSchemasForPlatform(normalized, provider.Platform); err != nil {
			return body, false, fmt.Errorf("normalize websocket tool schemas: %w", err)
		} else if schemaChanged {
			normalized = schemaBody
			changed = true
		}
	}
	// Earlier compatibility stages can filter or rebuild input. Reorder the remote
	// compaction trigger after them so it remains at the end of the forwarded history.
	if triggerBody, triggerChanged, err := openaiprotocol.NormalizeCompactionTriggerInputOrder(normalized); err != nil {
		return body, false, fmt.Errorf("normalize websocket compaction trigger order: %w", err)
	} else if triggerChanged {
		normalized = triggerBody
		changed = true
	}
	return normalized, changed, nil
}
