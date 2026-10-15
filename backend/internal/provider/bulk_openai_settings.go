package provider

import (
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// bulk_openai_settings.go validates bulk OpenAI capability and routing changes
// against requested settings and existing provider support.
// Bulk OpenAI edits can submit capability, route, and continuation settings
// before any target provider has been loaded. Presence flags keep a requested
// change separate from its normalized value, allowing target validation to
// inspect existing capabilities when the batch forces a text route.
//
// Canonical fields and their legacy aliases are processed in a fixed order,
// with a supplied legacy value replacing the corresponding summary flag. A
// workload set without text generation selects client-protocol routing unless
// the request also forces a text route, which is rejected. Validation runs
// before administration starts individual provider writes so these incompatible
// combinations fail before the batch changes any record.

// bulkOpenAISettings separates supplied OpenAI categories from their validated
// routing values.
type bulkOpenAISettings struct {
	// workloadCapabilities, textRouteMode, and continuationSupported retain key
	// presence, including null, so target validation still runs when the
	// normalized setting uses its default behavior.
	workloadCapabilities  bool // workload category was supplied
	textRouteMode         bool // text-route category was supplied
	continuationSupported bool // continuation category was supplied
	// capabilitiesIncludeText and forcedTextRoute summarize validation results.
	// Later legacy aliases overwrite the corresponding flag when both names
	// are supplied in the same update.
	capabilitiesIncludeText bool // selected workloads include text
	forcedTextRoute         bool // selection forces a text protocol
}

// any reports whether this batch contains an OpenAI configuration change.
func (s bulkOpenAISettings) any() bool {
	return s.workloadCapabilities || s.textRouteMode || s.continuationSupported
}

// normalizeBulkOpenAISettings validates batch OpenAI settings and records which
// categories were supplied. A nil input yields no change, while explicit null
// values retain their category's presence flag for later target checks.
//
// Legacy aliases are inspected after canonical keys and can replace the
// summarized capability or route value. A supplied workload selection without
// text generation rejects a forced route; otherwise this function mutates
// input.Extra to select client-protocol routing. Callers use the returned flags
// to validate every requested target before writing individual records.
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
		// A workload selection without text generation has no text endpoint to force.
		// Write client-protocol mode into the batch so later per-provider validation
		// receives the same combination that was checked here.
		input.Extra[ExtraKeyTextRouteMode] = string(TextRouteModePreserveClientProtocol)
		settings.textRouteMode = true
	}
	return settings, nil
}

// validateBulkOpenAIWorkloadCapabilities reports whether a supplied workload
// set includes text generation. Null means the default includes text; arrays
// and boolean maps accept text_generation, its chat_completions alias, and
// embeddings.
//
// Array entries and enabled map keys are trimmed and lowercased before
// matching. Disabled map entries contribute nothing, and an empty selection or
// an unsupported enabled value returns the shared bad-request error. Map values
// must be booleans even when their keys would otherwise be ignored.
func validateBulkOpenAIWorkloadCapabilities(raw any) (bool, error) {
	if raw == nil {
		return true, nil
	}
	selected := make(map[string]bool, 2)
	add := func(value string) error {
		switch strings.ToLower(strings.TrimSpace(value)) {
		// Legacy batch clients name the text workload chat_completions. Translate
		// that label to text_generation before the route compatibility check
		// decides whether a forced text endpoint can be selected.
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

// invalidBulkOpenAIWorkloadCapabilities builds the bad-request response for an
// unusable workload selection.
func invalidBulkOpenAIWorkloadCapabilities() error {
	return infraerrors.BadRequest(
		"OPENAI_WORKLOAD_CAPABILITIES_INVALID",
		"openai_workload_capabilities must contain text_generation, embeddings, or both",
	)
}

// validateBulkOpenAITextRouteMode reports whether the supplied route mode
// forces a text protocol. Null and preserve_client_protocol return false;
// legacy mode additionally accepts the exact string auto, while unsupported
// values return a bad-request error.
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

// invalidBulkOpenAITextRouteMode builds the bad-request response for an
// unsupported route value.
func invalidBulkOpenAITextRouteMode() error {
	return infraerrors.BadRequest(
		"OPENAI_TEXT_ROUTE_MODE_INVALID",
		"openai_text_route_mode must be preserve_client_protocol, force_responses, force_chat_completions, or null",
	)
}

// validateBulkOpenAIResponsesContinuationSupported accepts boolean or null
// continuation updates. Other dynamic types return the continuation-specific
// bad-request error, allowing the batch to reject the shared setting before
// checking provider eligibility.
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

// validateBulkOpenAISettingsTargets checks every requested ID before a batch
// writes OpenAI settings. A nil input or an unchanged category set succeeds
// immediately; otherwise each target must exist and be an OpenAI API Key
// record.
//
// A forced route can rely on the target's existing text capability when the
// summarized batch capability flag does not provide it. The first missing,
// ineligible, or incompatible target returns an error carrying that provider
// ID, so administration can reject the configuration before starting individual
// writes.
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

// invalidBulkOpenAITarget identifies the rejected batch target in its message
// and error metadata. The caller supplies the concrete validation reason, and
// provider_id is encoded as a decimal string for consumers of structured
// errors.
func invalidBulkOpenAITarget(providerID int64, message string) error {
	return infraerrors.BadRequest(
		"OPENAI_CONFIGURATION_TARGET_INVALID",
		fmt.Sprintf("provider %d: %s", providerID, message),
	).WithMetadata(map[string]string{"provider_id": strconv.FormatInt(providerID, 10)})
}
