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

// NormalizeResponsesLiteForProvider normalizes a Lite request for the provider's credential type.
func NormalizeResponsesLiteForProvider(value *providercore.Record, body []byte) ([]byte, bool, error) {
	if value == nil || value.Platform != capability.PlatformOpenAI {
		return body, false, nil
	}
	if value.IsOpenAIOAuth() {
		return openai.NormalizeResponsesLiteToolsPayload(body)
	}
	return openai.NormalizeResponsesLiteParallelToolCallsPayload(body)
}

func NormalizeOpenAIResponsesWebSocketCompatibilityBody(body []byte, provider *providercore.Record, responsesLite bool) ([]byte, bool, error) {
	if provider == nil || !provider.IsOpenAI() {
		return body, false, nil
	}
	normalized := body
	changed := false
	if provider.IsOpenAIOAuthLike() {
		var err error
		normalized, changed, err = NormalizeOpenAIResponsesLegacyIngress(body)
		if err != nil {
			return body, false, err
		}
	}
	if next, normalizedReasoningContent, err := openai.NormalizeOpenAIResponsesReasoningContentReplay(normalized); err != nil {
		return body, false, err
	} else if normalizedReasoningContent {
		normalized = next
		changed = true
	}
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
	if sanitized, idsChanged, err := SanitizeOpenAIResponsesInputItemIDs(normalized); err != nil {
		return body, false, fmt.Errorf("sanitize websocket Responses input item IDs: %w", err)
	} else if idsChanged {
		normalized = sanitized
		changed = true
	}
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
	// Compatibility processing may filter or rebuild input.
	// Append the remote compaction v2 trigger after that processing.
	if triggerBody, triggerChanged, err := openaiprotocol.NormalizeCompactionTriggerInputOrder(normalized); err != nil {
		return body, false, fmt.Errorf("normalize websocket compaction trigger order: %w", err)
	} else if triggerChanged {
		normalized = triggerBody
		changed = true
	}
	return normalized, changed, nil
}
