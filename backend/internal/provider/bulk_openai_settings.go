package provider

import (
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// bulkOpenAISettings tracks fields requiring bulk OpenAI API Key validation.
type bulkOpenAISettings struct {
	workloadCapabilities    bool
	textRouteMode           bool
	continuationSupported   bool
	capabilitiesIncludeText bool
	forcedTextRoute         bool
}

func (s bulkOpenAISettings) any() bool {
	return s.workloadCapabilities || s.textRouteMode || s.continuationSupported
}

// normalizeBulkOpenAISettings validates shared settings before checking each target's route combination.
// Long-context billing is not part of this provider configuration.
func normalizeBulkOpenAISettings(input *BulkUpdateProvidersInput) (bulkOpenAISettings, error) {
	var settings bulkOpenAISettings
	if input == nil {
		return settings, nil
	}

	if raw, exists := input.Credentials[OpenAIWorkloadCapabilitiesCredentialKey]; exists {
		settings.workloadCapabilities = true
		includeText, err := validateBulkOpenAIWorkloadCapabilities(raw)
		if err != nil {
			return settings, err
		}
		settings.capabilitiesIncludeText = includeText
	}
	if raw, exists := input.Credentials[LegacyOpenAICapabilitiesCredentialKey]; exists {
		settings.workloadCapabilities = true
		includeText, err := validateBulkOpenAIWorkloadCapabilities(raw)
		if err != nil {
			return settings, err
		}
		settings.capabilitiesIncludeText = includeText
	}

	if raw, exists := input.Extra[ExtraKeyTextRouteMode]; exists {
		settings.textRouteMode = true
		forced, err := validateBulkOpenAITextRouteMode(raw, false)
		if err != nil {
			return settings, err
		}
		settings.forcedTextRoute = forced
	}
	if raw, exists := input.Extra[LegacyOpenAIResponsesModeExtraKey]; exists {
		settings.textRouteMode = true
		forced, err := validateBulkOpenAITextRouteMode(raw, true)
		if err != nil {
			return settings, err
		}
		settings.forcedTextRoute = forced
	}
	if raw, exists := input.Extra[ExtraKeyResponsesContinuationSupported]; exists {
		settings.continuationSupported = true
		if err := validateBulkOpenAIResponsesContinuationSupported(raw); err != nil {
			return settings, err
		}
	}

	if settings.workloadCapabilities && !settings.capabilitiesIncludeText {
		if settings.forcedTextRoute {
			return settings, infraerrors.BadRequest(
				"OPENAI_TEXT_ROUTE_MODE_INVALID",
				"a forced text route requires the text_generation workload capability",
			)
		}
		if input.Extra == nil {
			input.Extra = make(map[string]any, 1)
		}
		// Embedding-only providers use the client protocol mode instead of a forced text route.
		input.Extra[ExtraKeyTextRouteMode] = string(TextRouteModePreserveClientProtocol)
		settings.textRouteMode = true
	}
	return settings, nil
}

func validateBulkOpenAIWorkloadCapabilities(raw any) (bool, error) {
	if raw == nil {
		return true, nil
	}
	selected := make(map[string]bool, 2)
	add := func(value string) error {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case string(OpenAIEndpointCapabilityTextGeneration), "chat_completions":
			selected[string(OpenAIEndpointCapabilityTextGeneration)] = true
		case string(OpenAIEndpointCapabilityEmbeddings):
			selected[string(OpenAIEndpointCapabilityEmbeddings)] = true
		default:
			return invalidBulkOpenAIWorkloadCapabilities()
		}
		return nil
	}

	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return false, invalidBulkOpenAIWorkloadCapabilities()
			}
			if err := add(value); err != nil {
				return false, err
			}
		}
	case []string:
		for _, value := range typed {
			if err := add(value); err != nil {
				return false, err
			}
		}
	case map[string]any:
		for key, value := range typed {
			selectedValue, ok := value.(bool)
			if !ok {
				return false, invalidBulkOpenAIWorkloadCapabilities()
			}
			if !selectedValue {
				continue
			}
			if err := add(key); err != nil {
				return false, err
			}
		}
	case map[string]bool:
		for key, selectedValue := range typed {
			if !selectedValue {
				continue
			}
			if err := add(key); err != nil {
				return false, err
			}
		}
	default:
		return false, invalidBulkOpenAIWorkloadCapabilities()
	}
	if len(selected) == 0 {
		return false, invalidBulkOpenAIWorkloadCapabilities()
	}
	return selected[string(OpenAIEndpointCapabilityTextGeneration)], nil
}

func invalidBulkOpenAIWorkloadCapabilities() error {
	return infraerrors.BadRequest(
		"OPENAI_WORKLOAD_CAPABILITIES_INVALID",
		"openai_workload_capabilities must contain text_generation, embeddings, or both",
	)
}

func validateBulkOpenAITextRouteMode(raw any, legacy bool) (bool, error) {
	if raw == nil {
		return false, nil
	}
	mode, ok := raw.(string)
	if !ok {
		return false, invalidBulkOpenAITextRouteMode()
	}
	if legacy && mode == "auto" {
		return false, nil
	}
	switch TextRouteMode(mode) {
	case TextRouteModeForceResponses, TextRouteModeForceChatCompletions:
		return true, nil
	case TextRouteModePreserveClientProtocol:
		return false, nil
	default:
		return false, invalidBulkOpenAITextRouteMode()
	}
}

func invalidBulkOpenAITextRouteMode() error {
	return infraerrors.BadRequest(
		"OPENAI_TEXT_ROUTE_MODE_INVALID",
		"openai_text_route_mode must be preserve_client_protocol, force_responses, force_chat_completions, or null",
	)
}

func validateBulkOpenAIResponsesContinuationSupported(raw any) error {
	if raw == nil {
		return nil
	}
	if _, ok := raw.(bool); !ok {
		return infraerrors.BadRequest(
			"OPENAI_RESPONSES_CONTINUATION_INVALID",
			"openai_responses_continuation_supported must be a boolean or null",
		)
	}
	return nil
}

// validateBulkOpenAISettingsTargets checks target IDs, API Key eligibility, and route combinations before writing.
func validateBulkOpenAISettingsTargets(input *BulkUpdateProvidersInput, settings bulkOpenAISettings, targetsByID map[int64]*Record) error {
	if input == nil || !settings.any() {
		return nil
	}
	for _, providerID := range input.ProviderIDs {
		provider, ok := targetsByID[providerID]
		if !ok || provider == nil {
			return invalidBulkOpenAITarget(providerID, "provider does not exist")
		}
		if !IsOpenAIAPIKeyProvider(provider) {
			return invalidBulkOpenAITarget(providerID, "workload capabilities, text route and continuation settings require an OpenAI API-key provider")
		}
		if settings.forcedTextRoute && !settings.capabilitiesIncludeText && !provider.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityTextGeneration, nil) {
			return invalidBulkOpenAITarget(providerID, "a forced text route requires the text_generation workload capability")
		}
	}
	return nil
}

func invalidBulkOpenAITarget(providerID int64, message string) error {
	return infraerrors.BadRequest(
		"OPENAI_CONFIGURATION_TARGET_INVALID",
		fmt.Sprintf("provider %d: %s", providerID, message),
	).WithMetadata(map[string]string{"provider_id": strconv.FormatInt(providerID, 10)})
}
