package protocol

import (
	"strings"
)

// Reasoning constants are wire values; routing owns administrator mappings and ordering policy.
const (
	ReasoningNone    = "none"
	ReasoningMinimal = "minimal"
	ReasoningLow     = "low"
	ReasoningMedium  = "medium"
	ReasoningHigh    = "high"
	ReasoningXHigh   = "xhigh"
	ReasoningMax     = "max"
)

// NormalizeClaudeOutputEffort normalizes Claude output_config.effort.
// Empty and unrecognized values return nil.
func NormalizeClaudeOutputEffort(raw string) *string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return nil
	}
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return &value
	default:
		return nil
	}
}

// OpenAIReasoningEfforts returns an independent slice of comparable effort levels in ascending order.
func OpenAIReasoningEfforts() []string {
	return []string{ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}
}

// AnthropicReasoningEfforts returns the recognized Anthropic effort levels.
func AnthropicReasoningEfforts() []string {
	return []string{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}
}

// OpenAIReasoningMappingValues prepends none to the ordered effort levels for mapping configuration.
func OpenAIReasoningMappingValues() []string {
	return append([]string{ReasoningNone}, OpenAIReasoningEfforts()...)
}
