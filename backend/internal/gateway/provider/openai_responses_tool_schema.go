package provider

import (
	"fmt"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Function-tool schemas are repaired according to the selected upstream
// platform before Responses forwarding. Root parameter type repair applies to
// OpenAI, Anthropic, Grok, and CN providers, while regex-pattern sanitization
// is limited to OpenAI; each stage returns the original body if it fails.

// ShouldRepairOpenAIResponsesNullToolSchemaType selects parameter-root type
// repair for OpenAI, Anthropic, Grok, and CN provider paths. Other platform
// names return false, leaving their schema values for that path's own
// handling.
func ShouldRepairOpenAIResponsesNullToolSchemaType(platform string) bool {
	return platform == capability.PlatformOpenAI || platform == capability.PlatformAnthropic || platform == capability.PlatformGrok || provider.IsCNProvider(platform)
}

// ShouldSanitizeOpenAIResponsesToolSchemaPatterns enables Responses
// regex-pattern sanitization for the OpenAI platform.
func ShouldSanitizeOpenAIResponsesToolSchemaPatterns(platform string) bool {
	return platform == capability.PlatformOpenAI
}

// SanitizeOpenAIResponsesToolSchemasForPlatform applies parameter-root repair
// before OpenAI-only pattern cleanup. The changed result combines successful
// stages; any stage error returns the original body and false, leaving partial
// repairs unpublished.
func SanitizeOpenAIResponsesToolSchemasForPlatform(body []byte, platform string) ([]byte, bool, error) {
	normalized := body
	changed := false
	// Repair the shared parameter-root type before platform-specific pattern rules
	// run, so both sanitizers inspect the same progressively normalized request.
	if ShouldRepairOpenAIResponsesNullToolSchemaType(platform) {
		next, repaired, err := wire.SanitizeToolParameterTypes(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool parameters: %w", err)
		}
		if repaired {
			normalized = next
			changed = true
		}
	}
	// Pattern cleanup is restricted to OpenAI; other supported platform paths retain
	// their regex content after the common parameter-root type repair.
	if ShouldSanitizeOpenAIResponsesToolSchemaPatterns(platform) {
		next, sanitized, err := wire.SanitizeToolSchemaPatterns(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool schema patterns: %w", err)
		}
		if sanitized {
			normalized = next
			changed = true
		}
	}
	return normalized, changed, nil
}
