package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Codex invitation and reset-credit requests use OAuth credentials with their
// own User-Agent and TLS settings. A shared token source can refresh the token
// before client construction, while a blank token falls back to the record's
// stored access token; the proxy is read separately from its ID.
//
// An enabled TLS router can override the invitation User-Agent and token TLS
// profile. A missing router profile resolves through the record's request TLS
// selection. The constructed client supplies account headers and, when a
// transport is configured, sends every invitation request through the chosen
// proxy and profile with provider-specific concurrency.

// CodexInviteFactory prepares authenticated invitation clients from provider
// records and transport dependencies.
type CodexInviteFactory struct {
	// Token optionally obtains a refreshed access token. Client uses the record
	// credential when the source is absent or returns blank.
	Token *provider.OpenAITokenSource
	// Proxy resolves a configured ProxyID. Supply this callback when the record
	// selects a proxy; Client propagates lookup errors.
	Proxy func(context.Context, int64) (*egress.Proxy, error)
	// Transport sends invitation requests through the selected proxy and profile.
	// When absent, the upstream client uses its default sender.
	Transport QoderTransport
	// Profiles resolves invitation and direct request fingerprints. A nil service
	// leaves the constructed client's selected TLS profile unset.
	Profiles *egressprovider.TLSProfiles
	// Routers optionally reads the configured runtime router. Client ignores
	// missing or disabled routers and keeps invitation defaults.
	Routers OpenAITokenRouterReader
}

// Client prepares a Codex invitation client for a nonnil provider record. It
// consults Token when configured and propagates refresh or proxy lookup errors;
// an empty resulting access token produces a bad-request error.
//
// An enabled router supplies invitation-specific User-Agent and TLS overrides,
// with record TLS selection used when its profile cannot be resolved. The
// client retains the record for account-header and transport callbacks, so
// callers keep that record stable while requests use the selected proxy,
// profile, ID, and concurrency.
func (f CodexInviteFactory) Client(ctx context.Context, value *provider.Record) (provider.CodexInviteClient, error) {
	token := ""
	if f.Token != nil {
		var err error
		token, err = f.Token.GetAccessToken(ctx, value)
		if err != nil {
			return nil, err
		}
	}
	// A blank result still permits stored OAuth credentials to authenticate
	// invitations, including factories built without the shared token source.
	// Token-source errors have already returned.
	if strings.TrimSpace(token) == "" {
		token = value.GetOpenAIAccessToken()
	}
	if strings.TrimSpace(token) == "" {
		return nil, apperror.BadRequest("CODEX_INVITE_RESET_MISSING_TOKEN", "missing OpenAI OAuth access token")
	}
	proxyURL := ""
	if value.ProxyID != nil {
		proxy, err := f.Proxy(ctx, *value.ProxyID)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	var router *egress.TLSFingerprintRouter
	if f.Routers != nil {
		router = f.Routers.GetRuntimeRouter(value.GetTLSFingerprintRouterID())
		if router != nil && !router.Enabled {
			router = nil
		}
	}
	userAgent := openai.CodexInviteDefaultUserAgent
	if router != nil && strings.TrimSpace(router.CodexInviteResetUserAgent) != "" {
		userAgent = strings.TrimSpace(router.CodexInviteResetUserAgent)
	}
	client := &openai.CodexInviteClient{
		Token: token, UserAgent: userAgent,
		ProviderHeaders: func(headers http.Header) {
			openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
		},
	}
	if f.Transport != nil {
		profile := resolveCodexInviteTLS(f.Profiles, value, router)
		client.Do = func(request *http.Request) (*http.Response, error) {
			return f.Transport.DoWithTLS(request, proxyURL, value.ID, value.Concurrency, profile)
		}
	}
	return client, nil
}

// resolveCodexInviteTLS chooses an invitation-specific router profile when it
// resolves successfully. Otherwise it uses the record's enabled request TLS
// selection; absent profile services return nil and leave transport
// fingerprinting unconfigured.
func resolveCodexInviteTLS(profiles *egressprovider.TLSProfiles, value *provider.Record, router *egress.TLSFingerprintRouter) *tlsfingerprint.Profile {
	if profiles == nil {
		return nil
	}
	if router != nil && router.CodexInviteResetTLSFingerprintProfileID != nil {
		if profile, ok := profiles.ResolveTokenTLSProfileByID(*router.CodexInviteResetTLSFingerprintProfileID); ok {
			return profile
		}
	}
	// An unresolved invitation profile uses the provider's request TLS selection,
	// so a missing router profile does not discard a configured direct
	// fingerprint.
	return profiles.ResolveRequestTLS(egress.TLSSelection{
		Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID(),
	})
}
