package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egresspostgres "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// grokImportQuotaProbe converts Grok quota results into import probe summaries.
type grokImportQuotaProbe struct{ source *provider.GrokQuotaService }

func (p grokImportQuotaProbe) QueryQuota(ctx context.Context, id int64) (*provider.GrokImportProbeResult, error) {
	value, err := p.source.QueryQuota(ctx, id)
	if value == nil {
		return nil, err
	}
	return &provider.GrokImportProbeResult{Model: value.Model, StatusCode: value.StatusCode, HeadersObserved: value.HeadersObserved}, err
}

// provideProviderArchive binds shared proxy, provider, probe, and privacy dependencies for file import and export.
func provideProviderArchive(admin *provider.Admin, proxies *egress.ProxyTransfer, privacy *provider.PrivacyService, probes *provider.GrokImportProbeScheduler, grok *provider.GrokQuotaService, tasks *lifecycle.Tasks) *provider.Archive {
	options := provider.ArchiveOptions{
		Now: time.Now, Info: slog.Info, Error: slog.Error, Debug: slog.Debug, DecodeIDToken: provideradapter.DecodeArchiveIDToken, Background: tasks.Go, ForcePrivacy: privacy.ForceAntigravityPrivacy,
		Probe: func(snapshot provider.ProviderSnapshot) {
			probes.Schedule(grokImportQuotaProbe{source: grok}, &snapshot)
		},
	}
	return provider.NewArchive(admin, proxies, options)
}

// provideCodexImporter binds provider administration, archive operations, and platform validation.
func provideCodexImporter(admin *provider.Admin, archive *provider.Archive, invalidator provider.TokenCacheInvalidator) *provider.CodexImporter {
	options := provider.CodexImportOptions{OAuthClientID: openai.ClientID, ValidatePrivateKey: func(value string) error { _, err := openai.ParseAgentIdentityPrivateKey(value); return err }}
	if invalidator != nil {
		options.Invalidate = func(ctx context.Context, value *provider.Record) error {
			return invalidator.InvalidateToken(ctx, value)
		}
	}
	options.Now = time.Now
	return provider.NewCodexImporter(admin, archive, options)
}

// provideCodexInvites shares request-side tokens and transport with invitation operations.
func provideCodexInvites(admin *provider.Admin, proxy egress.ProxyRepository, token *provider.OpenAITokenSource, transport httpclient.UpstreamTransport, profiles *egressprovider.TLSProfiles, routers provideradapter.OpenAITokenRouterReader) *provider.CodexInviteResetService {
	factory := provideradapter.CodexInviteFactory{Token: token, Proxy: proxy.GetByID, Transport: transport, Profiles: profiles, Routers: routers}
	return &provider.CodexInviteResetService{Options: provider.CodexInviteResetOptions{
		Read: admin.GetProvider, Client: factory.Client, NewID: uuid.NewString, Warn: slog.Warn,
	}}
}

// provideCRSSync binds shared provider storage, proxy storage, and refresh coordination for CRS synchronization.
func provideCRSSync(providers *providerpostgres.ProviderStore, proxies *egresspostgres.ProxyStore, oauth *provider.ClaudeAuthorization, openai *provider.OpenAIAuthorization, gemini *provider.GeminiAuthorization, cfg *config.Config, coordinator *provider.OAuthRefreshAPI) *provider.CRSSync {
	exchange := &provider.CRSAuthorization{Claude: oauth, OpenAI: openai, Gemini: gemini}
	v := cfg.Security.URLAllowlist
	client := provideradapter.NewCRSClient(provideradapter.CRSClientOptions{Configured: true, AllowlistEnabled: v.Enabled, AllowInsecureHTTP: v.AllowInsecureHTTP, AllowPrivateHosts: v.AllowPrivateHosts, Hosts: v.CRSHosts})
	return provider.NewCRSSync(providers, proxies, client, provider.CRSOptions{Now: time.Now, Warn: slog.Warn, Refresh: exchange.Coordinated(coordinator, provideradapter.GeminiTokenCacheKey)})
}

func provideCRSHTTP(source *provider.CRSSync) *providerhttp.CRSHandler {
	return providerhttp.NewCRSHandler(source)
}
