package capability

import "slices"

// ProviderProtocols contains a candidate's platform, type, authentication mode, and enabled protocols.
type ProviderProtocols struct {
	Platform, Type, AuthMode string
	Enabled                  []ProtocolID
}

// ResolveRoute prefers native execution, honors batch provider binding, and otherwise selects a single-step
// conversion.
func ResolveRoute(provider ProviderProtocols, source ProtocolID, fallbacks map[ProtocolID][]ProtocolID) (ProtocolID, bool) {
	enabled := provider.Enabled
	if slices.Contains(enabled, source) && slices.Contains(NativeProtocolOptions(provider.Platform, provider.Type, provider.AuthMode), source) {
		return source, true
	}
	if source == ProtocolImageBatches {
		// Batch jobs use the bound provider's dedicated upstream protocol.
		target := ProtocolGeminiBatch
		if provider.Type == ProviderTypeServiceAccount {
			target = ProtocolVertexBatch
		}
		return target, provider.Platform == PlatformGemini && slices.Contains(enabled, target)
	}
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
