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

// Upstream usage queries bind credentials and transport settings from one
// provider record after validating the query root. A configured proxy ID
// requires the corresponding loaded proxy and matching ID, so a missing or
// mismatched relation cannot silently send the credential through a direct
// connection.
//
// The constructed request captures the validated root, API key, wallet hints,
// proxy, and TLS profile. Header and sender callbacks retain the record for
// enabled overrides, ID, and concurrency, so the caller keeps that record
// stable during execution. Usage requests carry the OpenAI transport profile
// and disable redirects. The constructor copies the allowlist slice so later
// mutations of the supplied options do not change its URL policy.

// UsageHTTPOptions supplies query availability, validated URL policy, and
// provider-aware transport callbacks.
type UsageHTTPOptions struct {
	// Available enables query execution when Do is also supplied. A false value
	// keeps the adapter execution unavailable.
	Available bool
	// Policy validates usage roots and their allowed hosts. The constructor clones
	// UpstreamHosts before its request builder retains this policy.
	Policy egress.UsageURLPolicy
	// ResolveTLS optionally selects a request fingerprint from provider settings.
	// A nil callback leaves the captured TLS profile unset.
	ResolveTLS func(egress.TLSSelection) *tlsfingerprint.Profile
	// Do sends the request with its captured proxy, provider identity,
	// concurrency, and TLS profile. Availability is false when this callback is
	// absent.
	Do func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}

// NewUpstreamUsageHTTPExecution constructs native usage adapters with a copied
// URL allowlist. Availability requires both Available and Do; the returned
// request builder retains the remaining callback dependencies supplied in
// options.
func NewUpstreamUsageHTTPExecution(options UsageHTTPOptions) *UpstreamUsageExecution {
	// The request builder retains this policy across queries. Clone the caller's
	// host slice so later option edits cannot alter the accepted target hosts.
	options.Policy.UpstreamHosts = slices.Clone(options.Policy.UpstreamHosts)
	return NewUpstreamUsageExecution(UpstreamUsageExecutionOptions{
		Available: func() bool { return options.Available && options.Do != nil },
		BaseURL:   UpstreamUsageBaseURL,
		Request: func(value *provider.Record, config provider.UpstreamUsageQueryConfig) (*usagecontract.Request, error) {
			return usageHTTPRequest(value, config, options)
		},
	})
}

// usageHTTPRequest binds a usage adapter request to a nonnil provider record
// and query configuration. It requires a trimmed API key, validates the
// configured or platform root, and rejects missing or mismatched loaded proxy
// relations.
//
// Credentials, proxy URL, and TLS profile are captured at construction, while
// header and transport callbacks retain the provider record for overrides, ID,
// and concurrency. Keep that record stable until execution completes. The
// adapter supplies fixed endpoint paths and authentication order; the context
// callback selects the OpenAI transport profile and prevents redirects.
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
	// A selected proxy must be loaded and match the record's ProxyID. Reject stale
	// relations before creating callbacks that would otherwise send the API key
	// directly or through another proxy.
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
		// Adapter endpoints carry credentials and use fixed roots. Tag their
		// transport as OpenAI and disable redirects so a redirect cannot carry those
		// credentials to another target.
		Context: func(ctx context.Context) context.Context {
			return upstream.WithHTTPUpstreamRedirectsDisabled(upstream.WithHTTPUpstreamProfile(ctx, upstream.HTTPUpstreamProfileOpenAI))
		},
		Do: func(request *http.Request) (*http.Response, error) {
			return options.Do(request, proxyURL, value.ID, value.Concurrency, profile)
		},
	}, nil
}

// UpstreamUsageBaseURL returns the platform query root, leaving endpoint paths
// to native adapters. Nil records return empty text; known platforms apply
// their protocol defaults, while other platforms use the trimmed stored base
// URL.
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
