package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	invalidOpenAIServiceTierValueMaxLen = 64

	// ServiceTierPriority is the priority service tier used in upstream payloads.
	ServiceTierPriority  = "priority"
	ServiceTierUltrafast = "ultrafast"
	ServiceTierFlex      = "flex"
)

// InvalidServiceTierError reports an invalid service_tier for handlers to map to HTTP 400 invalid_request_error.
type InvalidServiceTierError struct {
	Value string
}

func NormalizeServiceTier(raw string) *string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return nil
	}
	if value == "fast" {
		value = "priority"
	}
	// Accept the service tiers used by Codex and direct API clients.
	// Codex can send priority, flex, or ultrafast; API clients also use
	// auto, default, and scale. Unknown values return nil so callers can
	// remove or reject the field according to their request policy.
	switch value {
	case "priority", "flex", "auto", "default", "scale", ServiceTierUltrafast:
		return &value
	default:
		return nil
	}
}

func (e *InvalidServiceTierError) Error() string {
	return fmt.Sprintf("invalid service_tier %q: must be one of auto, default, fast, flex, priority, scale, ultrafast", e.Value)
}

func boundInvalidOpenAIServiceTierValue(raw string) string {
	if len(raw) <= invalidOpenAIServiceTierValueMaxLen {
		return raw
	}
	return raw[:invalidOpenAIServiceTierValueMaxLen] + "..."
}

// ValidateServiceTierField validates and normalizes service_tier in an OpenAI-compatible body.
//
// Absent or null fields are accepted. fast maps to priority; other supported values
// are trimmed and lowercased. Empty strings, unknown values, and non-string fields fail.
func ValidateServiceTierField(body []byte) (string, error) {
	tierResult := gjson.GetBytes(body, "service_tier")
	if !tierResult.Exists() || tierResult.Type == gjson.Null {
		return "", nil
	}
	if tierResult.Type != gjson.String {
		return "", &InvalidServiceTierError{Value: "<non-string>"}
	}
	raw := strings.TrimSpace(tierResult.String())
	if raw == "" {
		return "", &InvalidServiceTierError{Value: raw}
	}
	norm := ServiceTierValue(raw)
	if norm == "" {
		return "", &InvalidServiceTierError{Value: boundInvalidOpenAIServiceTierValue(raw)}
	}
	return norm, nil
}

// ServiceTierValue returns the normalized tier, or an empty string for invalid input.
func ServiceTierValue(raw string) string {
	normalized := NormalizeServiceTier(raw)
	if normalized == nil {
		return ""
	}
	return *normalized
}
