package capability

import (
	"fmt"
	"slices"
)

// Saved protocol selections are checked against the platform and authentication
// choices used by route resolution. Empty selections retain an intentional
// disable, while conversion validation checks catalogue-supported targets and
// leaves actual provider availability to candidate resolution.

// NormalizeNativeProtocols validates enabled identifiers against this
// provider's native options and rejects duplicates. It returns a new slice in
// catalogue order; an empty selection stays allocated and empty.
func NormalizeNativeProtocols(provider ProviderProtocols) ([]ProtocolID, error) {
	options := NativeProtocolOptions(provider.Platform, provider.Type, provider.AuthMode)
	seen := make(map[ProtocolID]bool)
	for _, protocol := range provider.Enabled {
		if !slices.Contains(options, protocol) || seen[protocol] {
			return nil, fmt.Errorf("unsupported or duplicated native protocol %q", protocol)
		}
		seen[protocol] = true
	}
	normalized := []ProtocolID{}
	for _, protocol := range options {
		if seen[protocol] {
			normalized = append(normalized, protocol)
		}
	}
	return normalized, nil
}

// ValidateProtocolFallbacks rejects unknown client sources and duplicate or
// unsupported automatic conversion targets. Platform is ignored for mixed-group
// policy; empty target lists are valid and require native routing when used.
func ValidateProtocolFallbacks(_ string, fallbacks map[ProtocolID][]ProtocolID) error {
	for source, targets := range fallbacks {
		if !slices.Contains(canonicalGroupClientProtocols, source) {
			return fmt.Errorf("unknown source protocol %q", source)
		}
		seen := make(map[ProtocolID]bool)
		for _, target := range targets {
			if seen[target] || !slices.Contains(AutomaticProtocolFallbackTargets(source), target) {
				return fmt.Errorf("unsupported or duplicated conversion %s -> %s", source, target)
			}
			seen[target] = true
		}
	}
	return nil
}

// NormalizeResponsesImagePolicy validates the named Responses image-tool policy
// and maps an empty value to inherit. Other accepted values are returned
// unchanged; unknown spellings produce an error without trimming.
func NormalizeResponsesImagePolicy(policy string) (string, error) {
	switch policy {
	case "":
		return "inherit", nil
	case "inherit", "enabled", "disabled", "block":
		return policy, nil
	default:
		return "", fmt.Errorf("invalid Responses image policy")
	}
}
