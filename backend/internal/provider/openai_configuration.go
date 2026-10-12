package provider

import (
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	LegacyOpenAICapabilitiesCredentialKey = "openai_capabilities"
	LegacyOpenAIResponsesModeExtraKey     = "openai_responses_mode"
)

func IsOpenAIAPIKeyProvider(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformOpenAI && provider.Type == ProviderTypeAPIKey
}

func HasOpenAIConfigurationPatch(credentials, extra map[string]any) bool {
	if credentials != nil {
		if _, ok := credentials[OpenAIWorkloadCapabilitiesCredentialKey]; ok {
			return true
		}
		if _, ok := credentials[LegacyOpenAICapabilitiesCredentialKey]; ok {
			return true
		}
	}
	if extra == nil {
		return false
	}
	for _, key := range []string{
		ExtraKeyTextRouteMode,
		ExtraKeyResponsesContinuationSupported,
		LegacyOpenAIResponsesModeExtraKey,
	} {
		if _, ok := extra[key]; ok {
			return true
		}
	}
	return false
}

// NormalizeOpenAIAPIKeyConfiguration normalizes compaction settings and API-key workload and routing fields in
// place.
func NormalizeOpenAIAPIKeyConfiguration(provider *Record) error {
	if provider != nil && provider.IsOpenAI() {
		provider.Extra = maps.Clone(provider.Extra)
		if provider.Extra == nil {
			provider.Extra = map[string]any{}
		}
		DiscardDeprecatedProviderExtra(provider.Extra)
		provider.Extra["openai_compact_mode"] = provider.GetOpenAICompactMode()
		provider.Extra[OpenAINativeCompactionV2ModeExtraKey] = provider.GetOpenAINativeCompactionV2Mode()
	}
	if !IsOpenAIAPIKeyProvider(provider) {
		return nil
	}

	provider.Credentials = maps.Clone(provider.Credentials)
	if provider.Credentials == nil {
		provider.Credentials = make(map[string]any)
	}
	provider.Extra = maps.Clone(provider.Extra)
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}

	_, unified := provider.Credentials[UpstreamProtocolsKey]
	if err := NormalizeOpenAIWorkloadCapabilities(provider.Credentials, !unified); err != nil {
		return err
	}
	if err := NormalizeOpenAITextRouteMode(provider.Extra, !unified); err != nil {
		return err
	}
	DiscardDeprecatedProviderExtra(provider.Extra)
	return NormalizeOpenAIResponsesContinuationSupported(provider.Extra, true)
}

// NormalizeOpenAIAPIKeyConfigurationPatch normalizes supplied fields in place.
func NormalizeOpenAIAPIKeyConfigurationPatch(credentials, extra map[string]any) error {
	// Legacy keys in a patch represent edits from an older client and take priority over echoed new keys.
	// Full-record normalization instead prefers the stored new keys.
	if _, found := credentials[LegacyOpenAICapabilitiesCredentialKey]; found {
		delete(credentials, OpenAIWorkloadCapabilitiesCredentialKey)
	}
	if _, found := extra[LegacyOpenAIResponsesModeExtraKey]; found {
		delete(extra, ExtraKeyTextRouteMode)
	}
	if err := NormalizeOpenAIWorkloadCapabilities(credentials, false); err != nil {
		return err
	}
	if err := NormalizeOpenAITextRouteMode(extra, false); err != nil {
		return err
	}
	DiscardDeprecatedProviderExtra(extra)
	return NormalizeOpenAIResponsesContinuationSupported(extra, false)
}

func NormalizeOpenAIWorkloadCapabilities(credentials map[string]any, applyDefault bool) error {
	if credentials == nil {
		return nil
	}
	raw, found := credentials[OpenAIWorkloadCapabilitiesCredentialKey]
	if !found {
		raw, found = credentials[LegacyOpenAICapabilitiesCredentialKey]
	}
	if !found && !applyDefault {
		return nil
	}
	delete(credentials, LegacyOpenAICapabilitiesCredentialKey)
	if !found || raw == nil {
		credentials[OpenAIWorkloadCapabilitiesCredentialKey] = []string{
			string(OpenAIEndpointCapabilityTextGeneration),
			string(OpenAIEndpointCapabilityEmbeddings),
		}
		return nil
	}

	enabled := make(map[string]bool, 2)
	add := func(value string) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case string(OpenAIEndpointCapabilityTextGeneration), "chat_completions":
			enabled[string(OpenAIEndpointCapabilityTextGeneration)] = true
		case string(OpenAIEndpointCapabilityEmbeddings):
			enabled[string(OpenAIEndpointCapabilityEmbeddings)] = true
		}
	}
	switch capabilities := raw.(type) {
	case []any:
		for _, item := range capabilities {
			if value, ok := item.(string); ok {
				add(value)
			}
		}
	case []string:
		for _, value := range capabilities {
			add(value)
		}
	case map[string]any:
		for key, value := range capabilities {
			if selected, ok := value.(bool); ok && selected {
				add(key)
			}
		}
	case map[string]bool:
		for key, selected := range capabilities {
			if selected {
				add(key)
			}
		}
	default:
		return infraerrors.BadRequest(
			"OPENAI_WORKLOAD_CAPABILITIES_INVALID",
			"openai_workload_capabilities must be an array or object",
		)
	}

	normalized := make([]string, 0, 2)
	for _, capability := range []OpenAIEndpointCapability{
		OpenAIEndpointCapabilityTextGeneration,
		OpenAIEndpointCapabilityEmbeddings,
	} {
		if enabled[string(capability)] {
			normalized = append(normalized, string(capability))
		}
	}
	credentials[OpenAIWorkloadCapabilitiesCredentialKey] = normalized
	return nil
}

func NormalizeOpenAITextRouteMode(extra map[string]any, applyDefault bool) error {
	if extra == nil {
		return nil
	}
	raw, found := extra[ExtraKeyTextRouteMode]
	usingLegacy := false
	if !found {
		raw, found = extra[LegacyOpenAIResponsesModeExtraKey]
		usingLegacy = found
	}
	if !found && !applyDefault {
		return nil
	}
	delete(extra, LegacyOpenAIResponsesModeExtraKey)

	mode := TextRouteModePreserveClientProtocol
	if found {
		value, ok := raw.(string)
		if !ok {
			if !usingLegacy {
				return infraerrors.BadRequest(
					"OPENAI_TEXT_ROUTE_MODE_INVALID",
					"openai_text_route_mode must be a valid string",
				)
			}
		} else if usingLegacy {
			switch value {
			case string(TextRouteModeForceResponses):
				mode = TextRouteModeForceResponses
			case string(TextRouteModeForceChatCompletions):
				mode = TextRouteModeForceChatCompletions
			}
		} else {
			mode = NormalizeTextRouteMode(value)
			if value != string(mode) {
				return infraerrors.BadRequest(
					"OPENAI_TEXT_ROUTE_MODE_INVALID",
					"openai_text_route_mode is invalid",
				)
			}
		}
	}
	extra[ExtraKeyTextRouteMode] = string(mode)
	return nil
}

// NormalizeOpenAIResponsesContinuationSupported validates the HTTP continuation flag, mapping null to false.
func NormalizeOpenAIResponsesContinuationSupported(extra map[string]any, applyDefault bool) error {
	if extra == nil {
		return nil
	}
	raw, found := extra[ExtraKeyResponsesContinuationSupported]
	if !found && !applyDefault {
		return nil
	}
	if !found || raw == nil {
		extra[ExtraKeyResponsesContinuationSupported] = false
		return nil
	}
	supported, ok := raw.(bool)
	if !ok {
		return infraerrors.BadRequest(
			"OPENAI_RESPONSES_CONTINUATION_INVALID",
			"openai_responses_continuation_supported must be a boolean or null",
		)
	}
	extra[ExtraKeyResponsesContinuationSupported] = supported
	return nil
}
