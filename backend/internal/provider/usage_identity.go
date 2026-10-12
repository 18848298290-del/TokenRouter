package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

func UpstreamUsageContextFingerprint(provider *Record, config UpstreamUsageQueryConfig, baseURL string) string {
	if provider == nil {
		return "nil"
	}
	payload := struct {
		ID          int64
		Platform    string
		Type        string
		Credentials map[string]any
		Config      UpstreamUsageQueryConfig
		BaseURL     string
		ProxyID     *int64
		Proxy       any
		Concurrency int
		Transport   map[string]any
	}{
		ID:          provider.ID,
		Platform:    provider.Platform,
		Type:        provider.Type,
		Credentials: provider.Credentials,
		Config:      config,
		BaseURL:     baseURL,
		ProxyID:     provider.ProxyID,
		Concurrency: provider.Concurrency,
		Transport: map[string]any{
			"enable_tls_fingerprint":     usageExtraValue(provider.Extra, "enable_tls_fingerprint"),
			"tls_fingerprint_profile_id": usageExtraValue(provider.Extra, "tls_fingerprint_profile_id"),
			"tls_fingerprint_router_id":  usageExtraValue(provider.Extra, "tls_fingerprint_router_id"),
		},
	}
	if provider.Proxy != nil {
		// Include proxy credentials in the digest input so credential changes invalidate the identity.
		payload.Proxy = struct {
			ID       int64
			Protocol string
			Host     string
			Port     int
			Username string
			Password string
			Status   string
		}{provider.Proxy.ID, provider.Proxy.Protocol, provider.Proxy.Host, provider.Proxy.Port, provider.Proxy.Username, provider.Proxy.Password, provider.Proxy.Status}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%d:%s:%s:%v", provider.ID, provider.Platform, config.Adapter, provider.Credentials))
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func CNUsageMonitorIdentityFingerprint(provider *Record) string {
	if provider == nil || !provider.IsCNProvider() || provider.Type != ProviderTypeAPIKey {
		return ""
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		return ""
	}
	queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	if queryConfig.Adapter == "" {
		return ""
	}
	return UpstreamUsageContextFingerprint(provider, queryConfig, provider.OpenAIBaseURL(provider.ConfiguredAPIProtocol() == APIProtocolAdaptive))
}

func IsOllamaCloudUsageProvider(provider *Record) bool {
	if provider == nil || provider.Type != ProviderTypeAPIKey || (provider.Platform != PlatformOpenAI && provider.Platform != PlatformAnthropic) {
		return false
	}
	baseURL, _ := provider.Credentials["base_url"].(string)
	return egress.IsOllamaCloudBaseURL(baseURL)
}

func OllamaCloudUsageIdentity(provider *Record) map[string]any {
	if !IsOllamaCloudUsageProvider(provider) {
		return nil
	}
	apiKey, ok := provider.Credentials["api_key"].(string)
	if !ok || apiKey == "" {
		return nil
	}
	return map[string]any{"host": "ollama.com", "api_key": apiKey}
}

func OllamaCloudUsageGroupFingerprint(provider *Record) (string, bool) {
	identity := OllamaCloudUsageIdentity(provider)
	if identity == nil {
		return "", false
	}
	apiKey, _ := identity["api_key"].(string)
	sum := sha256.Sum256([]byte("ollama.com\x00" + apiKey))
	return hex.EncodeToString(sum[:]), true
}

func usageExtraValue(extra map[string]any, key string) any { return extra[key] }
