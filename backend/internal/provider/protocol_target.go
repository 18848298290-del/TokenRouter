package provider

import (
	"strings"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// protocol_target.go resolves CN protocol variants and endpoint addresses for
// a selected operation. CN endpoint selection combines stored provider
// configuration with one operation's text protocol. A recognized selected
// protocol overrides the stored variant, while a unified native-set key makes
// unselected CN operations adaptive even when the set is empty or malformed.
// Protocol admission is checked separately from these address and variant
// helpers.
//
// Adaptive and unified CN addresses use protocol-specific slots, with legacy
// base_url available as a Chat fallback. Fixed legacy Messages configurations
// instead use their stored base directly. Model and OpenAI-format endpoint
// lookup recognizes migrated Messages addresses, maps built-in addresses to
// platform Chat defaults, and strips a custom relay's terminal /anthropic
// path. Record configuration remains borrowed, and operation selection is
// carried separately rather than written back to shared provider state.

// ProtocolTarget combines borrowed provider configuration with one operation's
// selected text protocol for endpoint and variant resolution.
type ProtocolTarget struct {
	// Record remains borrowed during endpoint lookup. Callers keep its credentials
	// and platform stable while resolving an operation's URLs and variant.
	*Record
	// Protocol overrides the stored variant for recognized text identifiers with
	// a nonnil record. Empty or other identifiers defer to ConfiguredAPIProtocol.
	Protocol protocolcore.ProtocolID
}

// ConfiguredAPIProtocol selects the stored CN endpoint variant, with Chat as
// the default for nil, non-CN, or unrecognized configurations. A present
// upstream_protocols key selects adaptive lookup ahead of any legacy variant,
// including when that native selection is empty or malformed.
//
// With the unified key absent, trimmed legacy strings choose Messages, adaptive,
// or Chat directly. Legacy Responses also requires native platform support;
// Zhipu falls back to Chat. These rules choose endpoint lookup behavior, while
// UpstreamProtocols and route admission interpret the enabled native selection.
func (r *Record) ConfiguredAPIProtocol() string {
	if r == nil || !r.IsCNProvider() {
		return APIProtocolChatCompletions
	}
	// A unified set separates protocol admission from endpoint selection. Key
	// presence selects adaptive lookup even when parsing that selection would yield empty.
	if _, unified := r.Credentials[UpstreamProtocolsKey]; unified {
		return APIProtocolAdaptive
	}
	switch strings.TrimSpace(r.GetCredential("api_protocol")) {
	case APIProtocolAdaptive:
		return APIProtocolAdaptive
	case APIProtocolAnthropic:
		return APIProtocolAnthropic
	case APIProtocolResponses:
		// Legacy Responses selection uses the same platform predicate as forwarding.
		// Platforms lacking that endpoint fall through to the default Chat variant.
		if r.SupportsNativeCNResponses() {
			return APIProtocolResponses
		}
	case APIProtocolChatCompletions:
		return APIProtocolChatCompletions
	}
	return APIProtocolChatCompletions
}

// GetOpenAIBaseURL returns the OpenAI-format base selected by Record.OpenAIBaseURL
// with this operation's adaptive flag. Record must be nonnil. Address precedence
// and platform defaults belong to that helper; execution callers validate the
// returned URL before connecting.
func (a ProtocolTarget) GetOpenAIBaseURL() string {
	return a.OpenAIBaseURL(a.IsAdaptiveAPIProtocol())
}

// GetAPIProtocol selects a recognized operation text variant ahead of stored
// configuration for a nonnil record. Messages, Responses, and Chat identifiers
// override the record's variant even on a non-CN platform; other identifiers
// defer to ConfiguredAPIProtocol.
//
// A nil record resolves to Chat, while a unified CN record with no recognized
// operation selection resolves to adaptive. This helper does not validate native
// membership or execution support. Callers supply a separately admitted route
// before treating its selected variant as an executable endpoint.
func (a ProtocolTarget) GetAPIProtocol() string {
	if a.Record != nil && a.Protocol != "" {
		switch a.Protocol {
		case protocolcore.ProtocolAnthropicMessages:
			return APIProtocolAnthropic
		case protocolcore.ProtocolOpenAIResponses:
			return APIProtocolResponses
		case protocolcore.ProtocolOpenAIChatCompletions:
			return APIProtocolChatCompletions
		}
	}

	return a.ConfiguredAPIProtocol()
}

// UsesNativeCNResponses reports whether a supported CN platform resolves to
// Responses or adaptive lookup. Nil and unsupported records return false. This
// predicate checks platform and variant, so an empty unified selection can still
// return true; protocol admission checks its enabled identifiers separately.
func (a ProtocolTarget) UsesNativeCNResponses() bool {
	if a.Record == nil || !a.SupportsNativeCNResponses() {
		return false
	}
	switch a.GetAPIProtocol() {
	case APIProtocolResponses, APIProtocolAdaptive:
		return true
	default:
		return false
	}
}

// IsAdaptiveAPIProtocol reports whether variant resolution selects adaptive
// endpoint lookup. Nil records resolve to Chat and return false. A recognized
// operation text protocol overrides an adaptive stored configuration through
// GetAPIProtocol before this comparison.
func (a ProtocolTarget) IsAdaptiveAPIProtocol() bool {
	return a.GetAPIProtocol() == APIProtocolAdaptive
}

// GetCNProtocolBaseURL returns a configured or default CN address for protocol.
// Nil and non-CN records return empty. A unified key or adaptive operation first
// checks that exact protocol slot for a nonblank string, then Chat alone accepts
// a nonblank legacy base_url. Chosen custom strings are trimmed.
//
// Other cases use DefaultCNProtocolBaseURL's platform and provider-mode defaults,
// which return empty for unknown variants unless the earlier slot supplied an
// address. Fixed legacy lookup bypasses slots here; GetAnthropicProtocolBaseURL
// handles its stored Messages base separately. This helper selects an address
// and leaves URL and protocol admission validation to execution callers.
func (a ProtocolTarget) GetCNProtocolBaseURL(protocol string) string {
	if a.Record == nil || !a.IsCNProvider() {
		return ""
	}
	if _, unified := a.Credentials[UpstreamProtocolsKey]; unified || a.IsAdaptiveAPIProtocol() {
		if baseURLs, ok := a.Credentials["api_base_urls"].(map[string]any); ok {
			if baseURL, ok := baseURLs[protocol].(string); ok && strings.TrimSpace(baseURL) != "" {
				return strings.TrimSpace(baseURL)
			}
		}
		if protocol == APIProtocolChatCompletions {
			if baseURL := strings.TrimSpace(a.GetCredential("base_url")); baseURL != "" {
				return baseURL
			}
		}
	}
	// Missing usable slots fall back to platform and mode defaults. Legacy base_url
	// is a Chat fallback only, so another protocol does not inherit that relay address.
	return a.DefaultCNProtocolBaseURL(protocol)
}

// IsAnthropicProtocol reports whether variant resolution selects fixed Messages.
// Adaptive lookup returns false and is checked separately by endpoint callers.
// Nil records resolve to Chat, while a recognized operation Messages identifier
// takes precedence over stored configuration.
func (a ProtocolTarget) IsAnthropicProtocol() bool {
	return a.GetAPIProtocol() == APIProtocolAnthropic
}

// GetAnthropicProtocolBaseURL returns the Messages address for fixed Messages
// or adaptive lookup. Nil records and other resolved variants return empty.
// A present unified key or adaptive variant delegates to CN slot lookup, which
// also returns empty for a non-CN record.
//
// Fixed Messages lookup on API-key and upstream records prefers a trimmed
// base_url, including operation-selected Messages on a legacy record. Supported
// CN platforms then use their Messages defaults with Kimi's mode distinction.
// This keeps a legacy single-protocol relay address separate from unified slots.
// The helper performs no URL validation or enabled-selection check; execution
// callers establish those requirements before sending requests.
func (a ProtocolTarget) GetAnthropicProtocolBaseURL() string {
	if a.Record == nil || (!a.IsAnthropicProtocol() && !a.IsAdaptiveAPIProtocol()) {
		return ""
	}
	if _, unified := a.Credentials[UpstreamProtocolsKey]; unified || a.IsAdaptiveAPIProtocol() {
		return a.GetCNProtocolBaseURL(APIProtocolAnthropic)
	}
	// A fixed Messages lookup uses the record's single custom endpoint in base_url.
	// Prefer that value before platform defaults because it can identify a relay prefix.
	if a.Type == capability.ProviderTypeAPIKey || a.Type == capability.ProviderTypeUpstream {
		if baseURL := strings.TrimSpace(a.GetCredential("base_url")); baseURL != "" {
			return baseURL
		}
	}
	switch a.Platform {
	case capability.PlatformKimi:
		if a.GetProviderMode() == ProviderModeCoding {
			return DefaultKimiCodingAnthropicBaseURL
		}
		return DefaultKimiPayGAnthropicBaseURL
	case capability.PlatformZhipu:
		return DefaultZhipuAnthropicBaseURL
	case capability.PlatformDeepseek:
		return DefaultDeepseekAnthropicBaseURL
	default:
		return ""
	}
}

// GetOpenAIFormatBaseURL returns the model-list and OpenAI-format address for
// the selected provider, or empty for a nil record. CN Messages-derived addresses
// use platform Chat defaults for built-in endpoints. Custom bases delegate
// terminal /anthropic path removal to StripCNAnthropicPathSuffix.
//
// On unified CN records, raw slot strings identify a migrated Messages base:
// Chat is empty, Messages is nonempty, and Messages exactly equals base_url.
// These comparisons precede trimming and override the operation's Messages flag.
// Other configurations delegate to GetOpenAIBaseURL. Path stripping uses decoded
// URL paths and clears RawPath when removing the suffix, so escaping can change.
// Parse failures return trimmed input. Connecting callers validate the resulting
// address before use.
func (a ProtocolTarget) GetOpenAIFormatBaseURL() string {
	if a.Record == nil {
		return ""
	}

	// Migration retains base_url after moving a fixed Messages address into its
	// slot. Recognize that exact raw equality before deriving the model-list address;
	// a nonempty Chat slot selects ordinary Chat-base lookup even on a Messages operation.
	anthropicBase := a.IsAnthropicProtocol()
	if _, unified := a.Credentials[UpstreamProtocolsKey]; unified && a.IsCNProvider() {
		urls, _ := a.Credentials["api_base_urls"].(map[string]any)
		chat, _ := urls[APIProtocolChatCompletions].(string)
		messages, _ := urls[APIProtocolAnthropic].(string)
		anthropicBase = chat == "" && messages != "" && messages == a.GetCredential("base_url")
	}
	if !anthropicBase {
		return a.GetOpenAIBaseURL()
	}

	if baseURL := strings.TrimSpace(a.GetCredential("base_url")); baseURL != "" {
		// Custom relay prefixes can expose sibling Messages and OpenAI routes. Strip
		// the terminal Messages segment through the URL helper before model lookup;
		// that helper owns decoding, path escaping, and trailing-slash handling.
		if !IsDefaultCNAnthropicBaseURL(baseURL) {
			return StripCNAnthropicPathSuffix(baseURL)
		}
	}
	switch a.Platform {
	case capability.PlatformKimi:
		if a.GetProviderMode() == ProviderModeCoding {
			return DefaultKimiCodingBaseURL
		}
		return DefaultKimiPayGBaseURL
	case capability.PlatformZhipu:
		if a.GetProviderMode() == ProviderModeCoding {
			return DefaultZhipuCodingBaseURL
		}
		return DefaultZhipuPayGBaseURL
	case capability.PlatformDeepseek:
		return DefaultDeepseekBaseURL
	default:
		return a.GetOpenAIBaseURL()
	}
}
