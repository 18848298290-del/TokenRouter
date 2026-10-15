package provider

import (
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// input_validation.go validates platform credential choices and normalizes
// Grok media eligibility updates.
// CN provider credentials carry a mode and either legacy protocol selection or
// a unified protocol set. Creation records legacy defaults when needed, while
// editing can validate historical missing fields without writing those
// defaults. Platform-specific protocol checks reject combinations that the
// forwarding capability rules cannot serve.
//
// Grok media eligibility has a separate administrator override. An omitted
// update retains a saved boolean, explicit null removes the override, and a
// supplied boolean replaces it. Normalization copies the top-level map for this
// selection so deleting or restoring the override does not directly mutate the
// submitted settings. Other platforms keep their own Extra values untouched.

// NormalizeProviderConcurrency replaces nonpositive Grok OAuth concurrency with
// one. Other platform/type combinations and positive supplied values pass
// through unchanged, allowing creation and single editing to apply that
// platform default.
func NormalizeProviderConcurrency(platform, providerType string, concurrency int) int {
	if platform == PlatformGrok && providerType == ProviderTypeOAuth {
		if concurrency <= 0 {
			return 1
		}
	}
	return concurrency
}

// NormalizeCNProviderCredentials validates API Key type, mode, and legacy
// protocol for CN providers. Nil and non-CN records are skipped. Absent,
// non-string, or blank mode and protocol values are interpreted as payg and
// chat_completions. Creation stores an interpreted mode default regardless of
// unified protocol presence. An interpreted legacy protocol default is stored
// only when that unified key is absent.
//
// The credentials map is initialized when absent. Editing leaves missing
// default keys absent while still checking their interpreted values. Responses
// uses the provider's native platform capability check, and DeepSeek rejects
// coding mode before the record can be accepted.
// @project-doc docs/interfaces/upstream_provider_matrix.md#cn_provider_protocols
func NormalizeCNProviderCredentials(provider *Record, isCreate bool) error {
	if provider == nil || !IsCNProvider(provider.Platform) {
		return nil
	}
	if provider.Type != ProviderTypeAPIKey {
		return infraerrors.BadRequest("CN_PROVIDER_PROVIDER_TYPE_INVALID", "CN providers must use API Key credentials")
	}
	if provider.Credentials == nil {
		provider.Credentials = make(map[string]any)
	}
	mode, _ := provider.Credentials["provider_mode"].(string)
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = ProviderModePayG
		if isCreate {
			provider.Credentials["provider_mode"] = mode
		}
	}
	if mode != ProviderModePayG && mode != ProviderModeCoding {
		return infraerrors.BadRequest("CN_PROVIDER_PROVIDER_MODE_INVALID", "provider_mode must be payg or coding")
	}
	// Interpret historical missing protocol keys for validation. Creation persists
	// that legacy default only when a unified protocol set has not been supplied.
	protocol, _ := provider.Credentials["api_protocol"].(string)
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		protocol = APIProtocolChatCompletions
		if _, unified := provider.Credentials[UpstreamProtocolsKey]; isCreate && !unified {
			provider.Credentials["api_protocol"] = protocol
		}
	}
	switch protocol {
	case APIProtocolAdaptive, APIProtocolChatCompletions, APIProtocolAnthropic:
	case APIProtocolResponses:
		// Forwarding uses the same platform Responses capability. Reject a
		// legacy selection that cannot be served before it is saved as provider
		// configuration.
		if !provider.SupportsNativeCNResponses() {
			return infraerrors.BadRequest("CN_PROVIDER_PROTOCOL_INVALID", "only DeepSeek and Kimi support Responses protocol")
		}
	default:
		return infraerrors.BadRequest("CN_PROVIDER_PROTOCOL_INVALID", "api_protocol is unsupported")
	}
	if mode == ProviderModeCoding && provider.Platform == PlatformDeepseek {
		return infraerrors.BadRequest("CN_PROVIDER_MODE_INVALID", "DeepSeek does not support coding plan mode")
	}
	return nil
}

// ValidateGrokMediaEligibilityExtra validates a Grok media-eligibility override
// when present. A boolean is accepted, null requests removal during
// normalization, and nil settings or other platforms skip this
// provider-specific validation.
func ValidateGrokMediaEligibilityExtra(platform string, extra map[string]any) error {
	if platform != PlatformGrok || extra == nil {
		return nil
	}
	raw, exists := extra[GrokMediaEligibleExtraKey]
	if !exists || raw == nil {
		return nil
	}
	if _, ok := raw.(bool); !ok {
		return infraerrors.BadRequest(
			"GROK_MEDIA_ELIGIBILITY_INVALID",
			"grok_media_eligible must be a boolean or null",
		)
	}
	return nil
}

// NormalizeGrokMediaEligibilityExtra validates Grok settings and copies their
// top-level map before removing a null override. Non-Grok settings are returned
// as supplied, and nested values in a Grok copy remain shared.
func NormalizeGrokMediaEligibilityExtra(platform string, extra map[string]any) (map[string]any, error) {
	if platform != PlatformGrok {
		return extra, nil
	}
	if err := ValidateGrokMediaEligibilityExtra(platform, extra); err != nil {
		return nil, err
	}
	normalized := maps.Clone(extra)
	if normalized != nil && normalized[GrokMediaEligibleExtraKey] == nil {
		delete(normalized, GrokMediaEligibleExtraKey)
	}
	return normalized, nil
}

// NormalizeGrokMediaEligibilityUpdateExtra reconciles Grok's submitted override
// with the saved provider value. Explicit null removes the override, a supplied
// boolean uses the normalized input, and an omitted key carries forward a saved
// boolean. Invalid submitted types return an error.
//
// Grok callers supply a nonnil input and a normalized replacement already
// containing any submitted boolean override. The normalized outer map is copied
// before this decision and allocated when needed, retaining original input-key
// presence separately from the full replacement. Nil or non-Grok providers
// return the normalized map directly.
func NormalizeGrokMediaEligibilityUpdateExtra(provider *Record, input *UpdateProviderInput, normalized map[string]any) (map[string]any, error) {
	if provider == nil || provider.Platform != PlatformGrok {
		return normalized, nil
	}
	if err := ValidateGrokMediaEligibilityExtra(provider.Platform, input.Extra); err != nil {
		return nil, err
	}
	normalized = maps.Clone(normalized)
	if normalized == nil {
		normalized = make(map[string]any)
	}
	// Full Extra replacement loses omitted keys. Consult original input
	// presence before restoring the saved boolean so explicit null still clears
	// the override.
	raw, provided := input.Extra[GrokMediaEligibleExtraKey]
	if provided {
		if raw == nil {
			delete(normalized, GrokMediaEligibleExtraKey)
		}
		return normalized, nil
	}
	if current, ok := provider.Extra[GrokMediaEligibleExtraKey].(bool); ok {
		normalized[GrokMediaEligibleExtraKey] = current
	}
	return normalized, nil
}

// ValidateGeminiThirdPartyBaseURL requires third-party Gemini API Keys to supply
// a parsed URL with a scheme and hostname. It rejects GeminiOfficialAPIHost;
// nil records and other provider kinds skip this rule. Broader transport
// validation belongs to the request path.
func ValidateGeminiThirdPartyBaseURL(provider *Record) error {
	if provider == nil || !provider.IsGeminiThirdPartyProvider() || provider.HasGeminiThirdPartyBaseURL() {
		return nil
	}
	return infraerrors.BadRequest(
		"GEMINI_THIRD_PARTY_BASE_URL_REQUIRED",
		"Gemini third-party API Key providers require a non-Google base_url",
	)
}
