package provider

import (
	"fmt"
	"maps"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// protocols.go reads and normalizes provider native protocol selections,
// distinguishing missing sets from explicit empty selections. Missing sets
// derive legacy defaults, while malformed present sets produce empty read
// results. Parsing checks representation; saving also rejects duplicated,
// unknown, and platform-incompatible protocol identifiers.
//
// Save normalization orders the selection through the shared capability
// catalogue before interpreting OpenAI WebSocket compatibility settings. A
// legacy bridge can remove WebSocket from that validated selection.
// Credentials and Extra are updated in memory before persistence, so a later
// normalization error can leave partial record changes for the caller to
// discard. Legacy endpoint migration follows successful WebSocket
// normalization, retaining endpoint slots before removing the input fields
// that supplied them.

// UpstreamProtocolsKey stores the provider's selected native protocol array.
// Presence suppresses legacy defaults, including an explicit empty selection
// that disables admission.
const UpstreamProtocolsKey = "upstream_protocols"

// ProtocolAuthMode returns the OpenAI OAuth authentication variant used by
// protocol capability lookup. Nil and ordinary records return empty. Personal
// access token detection precedes agent identity when stored credential aliases
// make both predicates match.
func ProtocolAuthMode(provider *Record) string {
	if provider.IsOpenAIPersonalAccessToken() {
		return OpenAIAuthModePersonalAccessToken
	}
	if provider.IsOpenAIAgentIdentity() {
		return OpenAIAuthModeAgentIdentity
	}
	return ""
}

// NativeProtocolOptions returns a new slice of native capability options for
// the record's platform, type, and authentication mode. Nil records return a
// nonnil empty slice; unsupported combinations also have no options.
func (r *Record) NativeProtocolOptions() []capability.ProtocolID {
	if r == nil {
		return []capability.ProtocolID{}
	}
	return capability.NativeProtocolOptions(r.Platform, r.Type, ProtocolAuthMode(r))
}

// ParseProtocolSet returns a new protocol slice from ProtocolID, string, or any
// arrays. An any array requires plain string elements. Typed-nil arrays produce
// a nonnil empty slice; nil interfaces and other containers return errors.
// Identifiers retain spelling and order, with membership and duplication checked
// separately during save normalization.
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

// UpstreamProtocols returns configured native identifiers or defaults derived
// from the record's legacy variant. Nil records return a nonnil empty slice.
// Explicit empty and malformed present fields suppress default derivation, and
// callers validate identifiers separately before saving or route admission.
func (r *Record) UpstreamProtocols() []capability.ProtocolID {
	return r.UpstreamProtocolsForLegacy(r.ConfiguredAPIProtocol())
}

// UpstreamProtocolsForLegacy returns a parsed present selection or legacy
// defaults for legacyMode when the field is absent. Nil records return nonnil
// empty slices. Parsing retains identifier order and spelling; malformed fields
// return empty rather than substituting defaults, and typed-nil arrays remain empty.
func (r *Record) UpstreamProtocolsForLegacy(legacyMode string) []capability.ProtocolID {
	if r == nil {
		return []capability.ProtocolID{}
	}
	if raw, exists := r.Credentials[UpstreamProtocolsKey]; exists {
		protocols, err := ParseProtocolSet(raw)
		if err != nil {
			// A malformed present selection supplies no admitted native protocols.
			// Keep it distinct from absence so invalid saved data cannot activate defaults.
			return []capability.ProtocolID{}
		}
		return protocols
	}
	return r.LegacyUpstreamProtocols(legacyMode)
}

// NormalizeProviderProtocols validates and stores a canonical native selection
// in the supplied record before persistence. Nil records and credential shadows
// are skipped. Explicit arrays are checked for representation, then every
// selected identifier is checked against platform, type, and authentication
// options, with duplicates rejected and empty selections retained.
//
// The shared catalogue orders the saved slice before WebSocket compatibility
// migration can remove its WebSocket entry. Credentials are shallow-copied;
// nested values can remain shared, and an error from later WebSocket normalization
// can leave earlier changes on the record. Callers discard rejected candidates.
// Legacy fields are removed and endpoint slots migrated after all these checks
// succeed. Jev credential errors retain their own reason; selection errors use
// UPSTREAM_PROTOCOLS_INVALID. Invalid Images endpoint modes return their own
// IMAGE_STREAM_MODE_INVALID reason before protocol normalization mutates the record.
// @project-doc docs/interfaces/protocol_capabilities.md#account_native_protocols
func NormalizeProviderProtocols(provider *Record) error {
	if provider == nil || provider.IsCredentialShadow() {
		return nil
	}
	if err := ValidateJevCredentials(provider); err != nil {
		return err
	}
	if err := validateImageStreamModes(provider); err != nil {
		return err
	}
	protocols := provider.UpstreamProtocols()
	// Validate an explicit selection before interpreting legacy bridge switches.
	// Otherwise a bridge could hide an invalid or duplicated WebSocket identifier by deleting it.
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
	// Replace the top-level credential map after validation so the normalized
	// array does not rewrite a retained map's selection; nested unrelated values remain shared.
	provider.Credentials = maps.Clone(provider.Credentials)
	if provider.Credentials == nil {
		provider.Credentials = map[string]any{}
	}
	provider.Credentials[UpstreamProtocolsKey] = normalized
	// WebSocket normalization consumes legacy connection policy from the validated
	// selection. Migrate endpoints and remove legacy input fields only after it succeeds.
	if err := normalizeResponsesWS(provider); err != nil {
		return err
	}
	MigrateLegacyProtocolCredentials(provider)
	return nil
}
