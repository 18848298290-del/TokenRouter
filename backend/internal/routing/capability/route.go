package capability

import "slices"

// route.go selects a provider protocol from native capabilities and permitted
// conversion targets.
// A provider's enabled protocols declare possible upstream paths, and
// authentication limits which of those declarations are usable. Native
// execution wins before conversion, so explicit conversion restrictions cannot
// suppress an enabled valid native route.
//
// Missing source entries use the shared automatic target order; present empty
// entries require native execution, and populated entries preserve the
// configured conversion order. Batch image jobs instead use their bound Gemini
// candidate's dedicated batch protocol, choosing Vertex for service accounts.
// The result describes one protocol step; the surrounding request flow still
// validates models, admission, and execution eligibility.

// ProviderProtocols contains the credential-free inputs needed to evaluate one
// candidate's protocol route.
type ProviderProtocols struct {
	// Platform, Type, and AuthMode jointly select native protocol options and
	// conversion eligibility. Enabled is the saved selection that narrows those
	// supported options for this candidate.
	//
	// Platform: configured provider platform. Type: provider authentication type.
	// AuthMode: OpenAI authentication mode.
	Platform, Type, AuthMode string
	Enabled                  []ProtocolID // configured enabled upstream protocols
}

// ResolveRoute returns the preferred upstream protocol for one provider and
// client source, or false when no permitted path exists. Native execution
// requires both an enabled declaration and an option supported by the platform
// and authentication.
//
// For ordinary conversions, an absent source key selects automatic targets, an
// empty list permits none, and a populated list is tested in order. Batch
// images select the supplied Gemini provider's dedicated batch protocol
// regardless of fallback configuration. That branch checks platform and enabled
// protocol; callers supply validated authentication/protocol configuration.
// Client admission and model checks surround this pure protocol decision.
func ResolveRoute(provider ProviderProtocols, source ProtocolID, fallbacks map[ProtocolID][]ProtocolID) (ProtocolID, bool) {
	enabled := provider.Enabled
	if slices.Contains(enabled, source) && slices.Contains(NativeProtocolOptions(provider.Platform, provider.Type, provider.AuthMode), source) {
		return source, true
	}
	if source == ProtocolImageBatches {
		// Batch jobs use their dedicated protocol outside text conversion policy.
		// Service accounts choose Vertex; other types choose Gemini Batch, relying on
		// previously validated provider configuration.
		target := ProtocolGeminiBatch
		if provider.Type == ProviderTypeServiceAccount {
			target = ProtocolVertexBatch
		}
		return target, provider.Platform == PlatformGemini && slices.Contains(enabled, target)
	}
	// Absence means automatic matching; preserving a configured empty list lets
	// administrators require native execution even when converters exist.
	targets, configured := fallbacks[source]
	if !configured {
		targets = AutomaticProtocolFallbackTargets(source)
	}
	for _, target := range targets {
		if slices.Contains(enabled, target) && SupportsProtocolConversion(provider.Platform, provider.Type, provider.AuthMode, source, target) {
			return target, true
		}
	}
	return "", false
}
