package capability

import (
	"fmt"
)

// canonicalGroupClientProtocols derives ordering and validation coverage from the protocol catalog.
var canonicalGroupClientProtocols = func() []ProtocolID {
	out := []ProtocolID{}
	for _, protocol := range protocolCatalog {
		if !protocol.UpstreamOnly {
			out = append(out, protocol.ID)
		}
	}
	return out
}()

// SupportedGroupClientProtocols returns public entries; upstream eligibility is checked per provider.
func SupportedGroupClientProtocols(_ string) []ProtocolID {
	return append([]ProtocolID{}, canonicalGroupClientProtocols...)
}

// DefaultGroupClientProtocols enables the three text entries. Administrators opt in to other protocols.
func DefaultGroupClientProtocols(_ string) []ProtocolID {
	return []ProtocolID{ProtocolAnthropicMessages, ProtocolOpenAIResponses, ProtocolOpenAIChatCompletions}
}

// ValidateGroupClientProtocols validates the set and returns a copy in catalog order.
func ValidateGroupClientProtocols(platform string, protocols []ProtocolID) ([]ProtocolID, error) {
	supported := make(map[ProtocolID]struct{})
	for _, protocol := range SupportedGroupClientProtocols(platform) {
		supported[protocol] = struct{}{}
	}
	known := make(map[ProtocolID]struct{}, len(canonicalGroupClientProtocols))
	for _, protocol := range canonicalGroupClientProtocols {
		known[protocol] = struct{}{}
	}

	seen := make(map[ProtocolID]struct{}, len(protocols))
	for i, protocol := range protocols {
		if _, ok := known[protocol]; !ok {
			return nil, fmt.Errorf("allowed_protocols[%d] contains unknown protocol %q", i, protocol)
		}
		if _, ok := supported[protocol]; !ok {
			return nil, fmt.Errorf("protocol %q is not supported by platform %q", protocol, platform)
		}
		if _, ok := seen[protocol]; ok {
			return nil, fmt.Errorf("protocol %q is duplicated", protocol)
		}
		seen[protocol] = struct{}{}
	}
	out := make([]ProtocolID, 0, len(seen))
	for _, protocol := range canonicalGroupClientProtocols {
		if _, ok := seen[protocol]; ok {
			out = append(out, protocol)
		}
	}
	return out, nil
}

// SetGroupClientProtocol updates one selection and returns protocols in catalog order.
func SetGroupClientProtocol(protocols []ProtocolID, target ProtocolID, enabled bool) []ProtocolID {
	selected := make(map[ProtocolID]struct{}, len(protocols)+1)
	for _, protocol := range protocols {
		selected[protocol] = struct{}{}
	}
	if enabled {
		selected[target] = struct{}{}
	} else {
		delete(selected, target)
	}
	out := make([]ProtocolID, 0, len(selected))
	for _, protocol := range canonicalGroupClientProtocols {
		if _, ok := selected[protocol]; ok {
			out = append(out, protocol)
		}
	}
	return out
}

// DefaultProtocolFallbacks leaves entries absent for automatic matching; an explicit empty list requires
// native execution.
func DefaultProtocolFallbacks(_ string) map[ProtocolID][]ProtocolID {
	return map[ProtocolID][]ProtocolID{}
}
