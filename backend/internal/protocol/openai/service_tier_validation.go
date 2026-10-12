package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// Service-tier field validation distinguishes omitted or null values from
// explicit empty or unsupported values. Recognized strings are trimmed and
// lowercased, with fast represented by the canonical priority label; this local
// vocabulary does not establish a provider's execution or pricing capability.

const (
	// invalidOpenAIServiceTierValueMaxLen bounds the unsupported input prefix
	// echoed by validation. boundInvalidOpenAIServiceTierValue appends an ellipsis
	// when that prefix truncates the supplied value.
	invalidOpenAIServiceTierValueMaxLen = 64

	// These canonical labels let request preparation and tier policy compare
	// recognized values after normalization. The fast alias produces
	// ServiceTierPriority; flex and ultrafast retain their respective labels.
	ServiceTierPriority  = "priority"  // priority wire value
	ServiceTierUltrafast = "ultrafast" // ultrafast wire value
	ServiceTierFlex      = "flex"      // flex wire value
)

// InvalidServiceTierError reports an invalid service_tier that request
// handlers map to HTTP 400 invalid_request_error.
type InvalidServiceTierError struct {
	// Value supplies the description formatted by Error. ValidateServiceTierField
	// bounds unsupported string text and substitutes a nonstring placeholder;
	// callers constructing this error directly supply their own description.
	Value string
}

// NormalizeServiceTier returns a trimmed lowercase tier recognized by the
// local vocabulary, mapping fast to priority. Blank or unsupported labels
// return nil; callers decide whether their request policy omits or rejects
// an unresolved value.
func NormalizeServiceTier(raw string) *string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return nil
	}
	if value == "fast" {
		// The public fast alias uses priority as its validated representation.
		// Keep one canonical value while the invalid-value hint can still
		// advertise the accepted alias.
		value = "priority"
	}
	switch value {
	case "priority", "flex", "auto", "default", "scale", ServiceTierUltrafast:
		return &value
	default:
		return nil
	}
}

// Error formats the invalid-value description and accepted-label hint for
// request handlers.
func (e *InvalidServiceTierError) Error() string {
	return fmt.Sprintf("invalid service_tier %q: must be one of auto, default, fast, flex, priority, scale, ultrafast", e.Value)
}

// boundInvalidOpenAIServiceTierValue caps raw at 64 bytes and appends an ellipsis
// when truncation occurs. Shorter input is retained; the byte cap can split a
// multibyte character before Error quotes the bounded value.
func boundInvalidOpenAIServiceTierValue(raw string) string {
	if len(raw) <= invalidOpenAIServiceTierValueMaxLen {
		return raw
	}
	// Bound echoed input bytes before formatting the validation error.
	return raw[:invalidOpenAIServiceTierValueMaxLen] + "..."
}

// ValidateServiceTierField returns a recognized canonical service_tier or an
// InvalidServiceTierError. Missing and null fields return empty success; explicit
// empty, nonstring, or unsupported values fail. Fast normalizes to priority.
// Complete JSON validity is checked separately by the owning request parser.
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

// ServiceTierValue returns the canonical label selected by NormalizeServiceTier.
// Blank or unsupported input returns empty; this convenience result loses the
// distinction between those cases, leaving the caller to choose omission
// or rejection.
func ServiceTierValue(raw string) string {
	normalized := NormalizeServiceTier(raw)
	if normalized == nil {
		return ""
	}
	return *normalized
}
