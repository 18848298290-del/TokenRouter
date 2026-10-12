package requeststate

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// ThinkingRequestOptions carries the entry protocol and model capabilities used to filter thinking blocks.
type ThinkingRequestOptions struct {
	// Efforts is the resolved set of levels; nil uses the protocol defaults.
	Efforts                                 []string
	PreFilter, RetryFilters                 bool
	PassbackRequired, NativeReasoningEffort bool
	GLM, GLM53                              bool
	DummySignature                          string
}

func FilterThinkingBlocks(body []byte, options ThinkingRequestOptions) []byte {
	if !options.PreFilter {
		return body
	}
	return FilterThinkingBlocksInternal(body, options.DummySignature)
}

func FilterThinkingBlocksForRetry(body []byte, options ThinkingRequestOptions) []byte {
	if !options.RetryFilters {
		return body
	}
	return protocolanthropic.FilterThinkingBlocksForRetry(body)
}

func FilterSignatureSensitiveBlocksForRetry(body []byte, options ThinkingRequestOptions) []byte {
	if !options.RetryFilters {
		return body
	}
	return protocolanthropic.FilterSignatureSensitiveBlocksForRetry(body)
}

func FilterThinkingBlocksInternal(body []byte, dummySignature string) []byte {
	return protocolanthropic.FilterThinkingBlocksInternal(body, dummySignature)
}

func DefaultEffortForThinkingEnabled(options ThinkingRequestOptions) *string {
	if !options.PassbackRequired {
		return nil
	}
	if options.NativeReasoningEffort {
		// Providers with native reasoning_effort support choose their own default.
		return nil
	}
	effort := "high"
	return &effort
}

func OpenAIBodyHasThinkingEnabled(body []byte) bool {
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	return thinkingType == "enabled" || thinkingType == "adaptive"
}

func ApplyThinkingEnabledFallback(effort *string, body []byte, options ThinkingRequestOptions) *string {
	if effort != nil {
		return effort
	}
	if !OpenAIBodyHasThinkingEnabled(body) {
		return nil
	}
	return DefaultEffortForThinkingEnabled(options)
}

func NormalizeGLMOpenAIReasoningEffort(body []byte, options ThinkingRequestOptions) ([]byte, bool) {
	if !options.GLM {
		return body, false
	}

	path := "reasoning.effort"
	raw := strings.TrimSpace(gjson.GetBytes(body, path).String())
	if raw == "" {
		path = "reasoning_effort"
		raw = strings.TrimSpace(gjson.GetBytes(body, path).String())
	}
	if raw == "" {
		return body, false
	}

	mapped := NormalizeGLMEffortToken(raw)
	if options.GLM53 && mapped == "high" && NormalizeEffortToken(raw) == "low" {
		mapped = "low"
	}
	if options.Efforts != nil {
		mapped = configuredGLMEffort(raw, mapped, options.Efforts)
		if len(options.Efforts) == 0 {
			modified, err := sjson.DeleteBytes(body, path)
			if err != nil {
				return body, false
			}
			return modified, true
		}
	}
	if mapped == "" || mapped == raw {
		return body, false
	}

	modified, err := sjson.SetBytes(body, path, mapped)
	if err != nil {
		return body, false
	}
	return modified, true
}

func NormalizeEffortToken(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
}

func NormalizeGLMEffortToken(raw string) string {
	value := NormalizeEffortToken(raw)
	if value == "" {
		return ""
	}

	switch value {
	case "low", "medium", "high":
		return "high"
	case "xhigh", "extrahigh", "max", "ultracode":
		return "max"
	default:
		return ""
	}
}

func NormalizeGLM53AnthropicThinking(body []byte, options ThinkingRequestOptions) ([]byte, bool) {
	if !options.GLM53 {
		return body, false
	}

	raw := gjson.GetBytes(body, "output_config.effort").String()
	if strings.TrimSpace(raw) == "" {
		raw = gjson.GetBytes(body, "thinking.type").String()
	}

	var effort string
	switch NormalizeEffortToken(raw) {
	case "disabled", "off", "none", "minimal", "low":
		effort = "low"
	case "enabled", "adaptive", "medium", "high":
		effort = "high"
	case "xhigh", "max", "ultra":
		effort = "max"
	default:
		return body, false
	}
	if options.Efforts != nil {
		effort = configuredGLMEffort(raw, effort, options.Efforts)
	}

	modified, err := sjson.SetBytes(body, "thinking.type", "enabled")
	if err != nil {
		return body, false
	}
	if effort == "" {
		modified, err = sjson.DeleteBytes(modified, "output_config.effort")
	} else {
		modified, err = sjson.SetBytes(modified, "output_config.effort", effort)
	}
	if err != nil {
		return body, false
	}
	return modified, true
}

// configuredGLMEffort prefers an allowed explicit value, then the mapped effort.
// Otherwise it chooses the first allowed level at or above that effort, or the highest available level.
func configuredGLMEffort(raw, fallback string, allowed []string) string {
	value := NormalizeEffortToken(raw)
	if slices.Contains(allowed, value) {
		return value
	}
	if slices.Contains(allowed, fallback) {
		return fallback
	}
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	rank := slices.Index(levels, fallback)
	if rank < 0 {
		return ""
	}
	selected := ""
	for index, level := range levels {
		if !slices.Contains(allowed, level) {
			continue
		}
		selected = level
		if index >= rank {
			break
		}
	}
	if selected == "" && slices.Contains(allowed, "default") {
		return "default"
	}
	return selected
}

func NormalizeChineseLLMThinking(body []byte, minimaxAdaptive bool) ([]byte, bool) {
	if !minimaxAdaptive {
		return body, false
	}
	if gjson.GetBytes(body, "thinking.type").String() != "enabled" {
		return body, false
	}
	modified, err := sjson.SetBytes(body, "thinking.type", "adaptive")
	if err != nil {
		return body, false
	}
	return modified, true
}
