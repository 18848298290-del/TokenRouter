package provider

import (
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// OpenAI API-key compatibility input can name workloads through current or
// legacy fields, and an omitted field differs from a configured empty set.
// Full-record normalization supplies defaults when unified protocols are
// absent, while patch normalization changes supplied settings and gives legacy
// edit keys priority over echoed current keys. Empty workload containers
// remain empty so callers can represent disabled capabilities.
//
// Normalization mutates maps in stages and can return an error after earlier
// edits. Current route strings require exact supported values; legacy route
// values tolerate unknown or mistyped input by selecting the
// protocol-preserving mode. Compaction switches are normalized for every
// OpenAI provider before the API-key-specific work. These compatibility values
// feed later provider-protocol normalization; each caller decides whether to
// discard a partially normalized record after validation fails.

// Compatibility keys identify workload and route settings received from older
// clients before normalization rewrites them into the current provider configuration fields.
const (
	LegacyOpenAICapabilitiesCredentialKey = "openai_capabilities"   // legacy workload selection key
	LegacyOpenAIResponsesModeExtraKey     = "openai_responses_mode" // legacy text route key
)

// IsOpenAIAPIKeyProvider reports whether a nonnil record uses OpenAI API-key
// authentication.
func IsOpenAIAPIKeyProvider(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformOpenAI && provider.Type == ProviderTypeAPIKey
}

// HasOpenAIConfigurationPatch reports presence of current or legacy workload,
// text-route or continuation keys in supplied maps. Presence includes null and
// invalid values, allowing callers to detect an attempted edit before
// normalization validates its contents.
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

// NormalizeOpenAIAPIKeyConfiguration prepares an OpenAI record's compaction
// and API-key compatibility settings in place. Nil and other-platform records
// return nil; OpenAI records first receive normalized compaction switches
// regardless of authentication type.
//
// API-key credentials and Extra are shallow-copied before compatibility
// validation. Merely having UpstreamProtocolsKey suppresses workload and
// text-route defaults, while HTTP continuation still receives its false
// default. Without that key, missing workload selection enables text
// generation and embeddings, and missing text mode preserves the caller's
// protocol. Earlier mutations remain when a later validation fails, so callers
// use a disposable record when they need failure to leave their source
// unchanged.
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

// NormalizeOpenAIAPIKeyConfigurationPatch validates and rewrites supplied
// compatibility fields in the caller's maps. Nil maps and absent settings are
// skipped, while explicit null workload or continuation values receive their
// defined defaults.
//
// Legacy keys take priority in an edit: their presence deletes echoed current
// workload or text-route keys before normalization. Full-record normalization
// uses the opposite precedence when both remain stored. Workload, route and
// deprecated-field cleanup run before continuation validation, so an error can
// leave preceding edits applied. Callers preserve any desired input snapshot
// before invoking this mutating operation.
func NormalizeOpenAIAPIKeyConfigurationPatch(credentials, extra map[string]any) error {
	// Older clients can echo current fields alongside their edited legacy keys;
	// remove the echoed selections so those legacy edits reach the normalizers.
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

// NormalizeOpenAIWorkloadCapabilities rewrites supported workload selections
// as a deterministic string slice in credentials. Nil input is skipped;
// missing values are skipped when applyDefault is false, while missing
// defaulted values and explicit null enable text generation and embeddings.
//
// The current key takes precedence over the legacy key. Arrays retain
// recognized string entries, and objects retain recognized keys whose values
// are boolean true. The legacy chat_completions workload name becomes
// text_generation; unknown entries are ignored. Explicit empty or wholly
// unrecognized containers produce an empty slice, retaining their
// configured-empty meaning. Unsupported outer types return an error after the
// legacy key has been removed.
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
	// Capture the selected value before removing its legacy spelling so accepted
	// input can use the current key. Validation errors still leave the legacy key removed.
	delete(credentials, LegacyOpenAICapabilitiesCredentialKey)
	if !found || raw == nil {
		credentials[OpenAIWorkloadCapabilitiesCredentialKey] = []string{
			string(OpenAIEndpointCapabilityTextGeneration),
			string(OpenAIEndpointCapabilityEmbeddings),
		}
		return nil
	}

	enabled := make(map[string]bool, 2)
	// Restrict this compatibility set to recognized workload identities; unknown
	// names and nonstring array entries cannot introduce additional enabled workloads.
	add := func(value string) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		// Older workload lists use a protocol name for text generation; map it
		// to the workload identity accepted by current capability consumers.
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

	// Emit the two recognized workloads in fixed order; empty selections remain
	// configured empty so API-key consumers can distinguish them from missing settings.
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

// NormalizeOpenAITextRouteMode validates the compatibility text-route field
// and writes its canonical string in Extra. Nil input and an omitted,
// nondefaulted setting are skipped; a missing defaulted setting preserves the
// caller's protocol.
//
// The current key takes precedence over the legacy key for stored records.
// Current values require a string exactly equal to a supported mode, including
// case and whitespace. Legacy values select a forced mode only for its exact
// string; other strings and mistyped values become protocol-preserving mode.
// The legacy key is deleted before validation, so a failing current value can
// still leave that mutation applied.
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
	// The selected route is captured before deleting its legacy spelling. Accepted
	// values use the current key, while failed validation retains the earlier deletion.
	delete(extra, LegacyOpenAIResponsesModeExtraKey)

	mode := TextRouteModePreserveClientProtocol
	if found {
		value, ok := raw.(string)
		if !ok {
			// Current field types are validated for administrator feedback; legacy
			// mistyped values continue to the protocol-preserving mode used by older input.
			if !usingLegacy {
				return infraerrors.BadRequest(
					"OPENAI_TEXT_ROUTE_MODE_INVALID",
					"openai_text_route_mode must be a valid string",
				)
			}
		} else if usingLegacy {
			// Unrecognized legacy values select the preserving mode, while exact forced
			// strings carry their older client's endpoint preference into current configuration.
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

// NormalizeOpenAIResponsesContinuationSupported validates the HTTP
// continuation boolean in Extra. Nil maps and omitted, nondefaulted fields are
// skipped; explicit null and a missing defaulted field become false, while
// other nonboolean values return an error.
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
