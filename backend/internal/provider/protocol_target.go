package provider

import (
	"strings"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ProtocolTarget combines a per-operation protocol selection with provider configuration.
// An empty Protocol uses the configured protocol; this selection is not stored on Record.
type ProtocolTarget struct {
	*Record
	Protocol protocolcore.ProtocolID
}

// ConfiguredAPIProtocol reads the CN protocol setting, defaulting other or invalid configurations to Chat
// Completions.
func (r *Record) ConfiguredAPIProtocol() string {
	if r == nil || !r.IsCNProvider() {
		return APIProtocolChatCompletions
	}
	if _, unified := r.Credentials[UpstreamProtocolsKey]; unified {
		return APIProtocolAdaptive
	}
	switch strings.TrimSpace(r.GetCredential("api_protocol")) {
	case APIProtocolAdaptive:
		return APIProtocolAdaptive
	case APIProtocolAnthropic:
		return APIProtocolAnthropic
	case APIProtocolResponses:
		if r.SupportsNativeCNResponses() {
			return APIProtocolResponses
		}
	case APIProtocolChatCompletions:
		return APIProtocolChatCompletions
	}
	return APIProtocolChatCompletions
}

// GetOpenAIBaseURL resolves the OpenAI base URL using this operation's adaptive-protocol setting.
func (a ProtocolTarget) GetOpenAIBaseURL() string {
	return a.OpenAIBaseURL(a.IsAdaptiveAPIProtocol())
}

// GetAPIProtocol returns the selected protocol variant when recognized, or the configured variant otherwise.
// Unified CN protocol sets resolve to adaptive mode when no operation-specific variant is selected.
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

// UsesNativeCNResponses reports whether a platform with native Responses support
// is configured for Responses or adaptive routing.
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

// IsAdaptiveAPIProtocol reports whether endpoint selection follows the inbound protocol.
func (a ProtocolTarget) IsAdaptiveAPIProtocol() bool {
	return a.GetAPIProtocol() == APIProtocolAdaptive
}

// GetCNProtocolBaseURL returns the CN platform's base URL for a protocol.
// Unified or adaptive configurations prefer api_base_urls; Chat also accepts legacy base_url.
// Otherwise the platform and provider mode determine the default endpoint.
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
	return a.DefaultCNProtocolBaseURL(protocol)
}

// IsAnthropicProtocol reports whether the selected variant is Anthropic Messages.
// Adaptive selection is checked separately by callers.
func (a ProtocolTarget) IsAnthropicProtocol() bool {
	return a.GetAPIProtocol() == APIProtocolAnthropic
}

// GetAnthropicProtocolBaseURL resolves a Messages endpoint for Anthropic or adaptive variants.
// Unified or adaptive CN configurations use protocol-specific slots; legacy API-key and upstream records
// prefer base_url.
// Otherwise platform and provider mode determine the default; unsupported variants return empty.
func (a ProtocolTarget) GetAnthropicProtocolBaseURL() string {
	if a.Record == nil || (!a.IsAnthropicProtocol() && !a.IsAdaptiveAPIProtocol()) {
		return ""
	}
	if _, unified := a.Credentials[UpstreamProtocolsKey]; unified || a.IsAdaptiveAPIProtocol() {
		return a.GetCNProtocolBaseURL(APIProtocolAnthropic)
	}
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

// GetOpenAIFormatBaseURL resolves a base for OpenAI-format model and Chat endpoints.
// Non-Messages configurations use GetOpenAIBaseURL. Messages defaults map to the platform's OpenAI base.
// For custom Messages relays, remove the trailing /anthropic segment
// while retaining the relay host and preceding path.
func (a ProtocolTarget) GetOpenAIFormatBaseURL() string {
	if a.Record == nil {
		return ""
	}

	// Detect a migrated fixed-Messages base through the protocol slots before deriving the model-list base.
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
