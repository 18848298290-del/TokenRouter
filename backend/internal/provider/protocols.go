package provider

import (
	"fmt"
	"maps"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const UpstreamProtocolsKey = "upstream_protocols"

func ProtocolAuthMode(provider *Record) string {
	if provider.IsOpenAIPersonalAccessToken() {
		return OpenAIAuthModePersonalAccessToken
	}
	if provider.IsOpenAIAgentIdentity() {
		return OpenAIAuthModeAgentIdentity
	}
	return ""
}

// NativeProtocolOptions lists available protocols for the provider's platform, type, and authentication mode.
func (r *Record) NativeProtocolOptions() []capability.ProtocolID {
	if r == nil {
		return []capability.ProtocolID{}
	}
	return capability.NativeProtocolOptions(r.Platform, r.Type, ProtocolAuthMode(r))
}

func ParseProtocolSet(raw any) ([]capability.ProtocolID, error) {
	out := []capability.ProtocolID{}
	switch value := raw.(type) {
	case []capability.ProtocolID:
		out = append(out, value...)
	case []string:
		for _, item := range value {
			out = append(out, capability.ProtocolID(item))
		}
	case []any:
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("upstream_protocols must contain strings")
			}
			out = append(out, capability.ProtocolID(text))
		}
	default:
		return nil, fmt.Errorf("upstream_protocols must be an array")
	}
	return out, nil
}

// UpstreamProtocols reads the configured set, deriving defaults when the field is absent.
func (r *Record) UpstreamProtocols() []capability.ProtocolID {
	return r.UpstreamProtocolsForLegacy(r.ConfiguredAPIProtocol())
}

// UpstreamProtocolsForLegacy uses the supplied legacy variant when the protocol set is absent; malformed sets
// yield empty.
func (r *Record) UpstreamProtocolsForLegacy(legacyMode string) []capability.ProtocolID {
	if r == nil {
		return []capability.ProtocolID{}
	}
	if raw, exists := r.Credentials[UpstreamProtocolsKey]; exists {
		protocols, err := ParseProtocolSet(raw)
		if err != nil {
			return []capability.ProtocolID{}
		}
		return protocols
	}
	return r.LegacyUpstreamProtocols(legacyMode)
}

// NormalizeProviderProtocols validates protocols for saving; an explicit empty set disables new calls.
// @project-doc docs/interfaces/protocol_capabilities.md#account_native_protocols
func NormalizeProviderProtocols(provider *Record) error {
	if provider == nil || provider.IsCredentialShadow() {
		return nil
	}
	if err := ValidateJevCredentials(provider); err != nil {
		return err
	}
	protocols := provider.UpstreamProtocols()
	if raw, exists := provider.Credentials[UpstreamProtocolsKey]; exists {
		var err error
		protocols, err = ParseProtocolSet(raw)
		if err != nil {
			return infraerrors.BadRequest("UPSTREAM_PROTOCOLS_INVALID", err.Error())
		}
	}
	normalized, err := capability.NormalizeNativeProtocols(capability.ProviderProtocols{Platform: provider.Platform, Type: provider.Type, AuthMode: ProtocolAuthMode(provider), Enabled: protocols})
	if err != nil {
		return infraerrors.BadRequest("UPSTREAM_PROTOCOLS_INVALID", err.Error())
	}
	provider.Credentials = maps.Clone(provider.Credentials)
	if provider.Credentials == nil {
		provider.Credentials = map[string]any{}
	}
	provider.Credentials[UpstreamProtocolsKey] = normalized
	if err := normalizeResponsesWS(provider); err != nil {
		return err
	}
	MigrateLegacyProtocolCredentials(provider)
	return nil
}
