package provider

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
)

// UsageHTTPOptions supplies usage-query transport and outbound URL policy.
type UsageHTTPOptions struct {
	Available  bool
	Policy     egress.UsageURLPolicy
	ResolveTLS func(egress.TLSSelection) *tlsfingerprint.Profile
	Do         func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}

func NewUpstreamUsageHTTPExecution(options UsageHTTPOptions) *UpstreamUsageExecution {
	options.Policy.UpstreamHosts = slices.Clone(options.Policy.UpstreamHosts)
	return NewUpstreamUsageExecution(UpstreamUsageExecutionOptions{
		Available: func() bool { return options.Available && options.Do != nil },
		BaseURL:   UpstreamUsageBaseURL,
		Request: func(value *provider.Record, config provider.UpstreamUsageQueryConfig) (*usagecontract.Request, error) {
			return usageHTTPRequest(value, config, options)
		},
	})
}

func usageHTTPRequest(value *provider.Record, query provider.UpstreamUsageQueryConfig, options UsageHTTPOptions) (*usagecontract.Request, error) {
	apiKey := strings.TrimSpace(value.GetCredential("api_key"))
	if apiKey == "" {
		return nil, provider.ErrUpstreamUsageProviderInvalid
	}
	base := strings.TrimSpace(query.BaseURL)
	if base == "" {
		base = UpstreamUsageBaseURL(value)
	}
	validated, err := options.Policy.Validate(base)
	if err != nil {
		return nil, provider.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	proxyURL := ""
	if value.ProxyID != nil {
		if value.Proxy == nil {
			return nil, provider.ErrUpstreamUsageRequestFailed
		}
		if value.Proxy.ID != *value.ProxyID {
			return nil, provider.ErrUpstreamUsageIdentityChanged
		}
		proxyURL = value.Proxy.URL()
	}
	var profile *tlsfingerprint.Profile
	if options.ResolveTLS != nil {
		profile = options.ResolveTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
	}
	return &usagecontract.Request{
		BaseURL: validated, APIKey: apiKey,
		WalletToken:       value.GetCredential(provider.NewAPIUserAccessTokenCredentialKey),
		WalletUserID:      value.GetCredential(provider.NewAPIUserIDCredentialKey),
		ZhipuOrganization: value.GetCredential("zhipu_organization"), ZhipuProject: value.GetCredential("zhipu_project"),
		ApplyHeaders: func(headers http.Header) {
			ApplyProviderHeaderOverrides(value, headers)
		},
		Endpoint: httpclient.BuildOpenAIEndpointURL,
		Context: func(ctx context.Context) context.Context {
			return upstream.WithHTTPUpstreamRedirectsDisabled(upstream.WithHTTPUpstreamProfile(ctx, upstream.HTTPUpstreamProfileOpenAI))
		},
		Do: func(request *http.Request) (*http.Response, error) {
			return options.Do(request, proxyURL, value.ID, value.Concurrency, profile)
		},
	}, nil
}

// UpstreamUsageBaseURL selects a platform root; the usage adapter supplies its endpoint path.
func UpstreamUsageBaseURL(value *provider.Record) string {
	if value == nil {
		return ""
	}
	switch value.Platform {
	case capability.PlatformOpenAI, capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek:
		return (provider.ProtocolTarget{Record: value}).GetOpenAIBaseURL()
	case capability.PlatformAnthropic:
		return value.GetBaseURL()
	case capability.PlatformGrok:
		return grok.ResolveProviderBaseURL(value.IsGrokOAuth(), value.GetCredential("base_url"), "")
	case capability.PlatformGemini, capability.PlatformAntigravity:
		return value.GetGeminiBaseURL("https://generativelanguage.googleapis.com")
	default:
		return strings.TrimSpace(value.GetCredential("base_url"))
	}
}
