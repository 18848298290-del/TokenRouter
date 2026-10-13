package capability

import (
	"fmt"
)

// Group client admission is independent of provider platform selection. Public
// entries come from the protocol catalogue, excluding upstream-only operations,
// so mixed groups can expose the same ordered choices while provider route
// resolution decides which candidates can execute each request.

// canonicalGroupClientProtocols retains public protocol catalogue order for
// group selection and validation; upstream-only identifiers stay outside this
// admission list.
var canonicalGroupClientProtocols = func() []ProtocolID {
	out := []ProtocolID{}
	for _, protocol := range protocolCatalog {
		if !protocol.UpstreamOnly {
			out = append(out, protocol.ID)
		}
	}
	return out
}()

// SupportedGroupClientProtocols returns a new slice of public protocol
// identifiers in catalogue order. The platform argument is retained for
// callers, and every group receives the same choices.
func SupportedGroupClientProtocols(_ string) []ProtocolID {
	return append([]ProtocolID{}, canonicalGroupClientProtocols...)
}

// DefaultGroupClientProtocols returns the three initially admitted text entries
// in a new slice. The platform argument does not affect these defaults;
// administrators separately enable other operations.
func DefaultGroupClientProtocols(_ string) []ProtocolID {
	return []ProtocolID{ProtocolAnthropicMessages, ProtocolOpenAIResponses, ProtocolOpenAIChatCompletions}
}

// ValidateGroupClientProtocols rejects unknown or duplicate client identifiers
// and returns a new slice in catalogue order. Platform does not restrict group
// admission, and an empty input produces an allocated empty selection.
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

// SetGroupClientProtocol enables or removes target and returns a new
// catalogue-ordered selection. Input duplicates and unknown identifiers are
// discarded during rebuilding, and the input slice remains unchanged.
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

// DefaultProtocolFallbacks returns a new empty conversion map for
// platform-independent group defaults. Missing source entries allow automatic
// conversion matching; callers insert an explicit empty list to require native
// execution.
func DefaultProtocolFallbacks(_ string) map[ProtocolID][]ProtocolID {
	return map[ProtocolID][]ProtocolID{}
}
