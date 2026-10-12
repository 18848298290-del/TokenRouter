package provider

import (
	"strings"
)

// OpenAIBaseURL resolves the upstream base URL for OpenAI and CN providers.
// CN providers include Kimi, Zhipu, and DeepSeek. Grok uses GetGrokBaseURL;
// this method returns empty for Grok and other unsupported platforms.
func (r *Record) OpenAIBaseURL(adaptive bool) string {
	if !r.IsOpenAI() && !r.IsCNProvider() {
		return ""
	}
	if _, unified := r.Credentials[UpstreamProtocolsKey]; r.IsCNProvider() && (unified || adaptive) {
		if baseURLs, ok := r.Credentials["api_base_urls"].(map[string]any); ok {
			if baseURL, ok := baseURLs[APIProtocolChatCompletions].(string); ok && strings.TrimSpace(baseURL) != "" {
				return strings.TrimSpace(baseURL)
			}
		}
	}
	if r.Type == ProviderTypeAPIKey || r.Type == ProviderTypeUpstream {
		if baseURL := strings.TrimSpace(r.GetCredential("base_url")); baseURL != "" {
			return baseURL
		}
	}
	// CN defaults depend on the payg or coding provider mode.
	switch r.Platform {
	case PlatformKimi:
		if r.GetProviderMode() == ProviderModeCoding {
			return DefaultKimiCodingBaseURL
		}
		return DefaultKimiPayGBaseURL
	case PlatformZhipu:
		if r.GetProviderMode() == ProviderModeCoding {
			return DefaultZhipuCodingBaseURL
		}
		return DefaultZhipuPayGBaseURL
	case PlatformDeepseek:
		return DefaultDeepseekBaseURL
	default:
		return "https://api.openai.com"
	}
}
