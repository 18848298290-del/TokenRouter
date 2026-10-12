package app

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// provideModelAttributes supplies model attributes for administration and display.
func provideModelAttributes(repo *postgres.ModelAttributeStore, catalog *provider.Service, invalidator apikey.APIKeyAuthCacheInvalidator, catalogue *routing.RequestableCatalogue) *routing.ModelAttributeService {
	return &routing.ModelAttributeService{
		Repo:        repo,
		Invalidator: invalidator,
		Catalogue:   catalogue,
		Catalog: routing.ModelAttributeCatalog{
			View: catalog.CapabilityView,
			Index: func() (modelcatalog.Index, routing.ModelAttributeSnapshot) {
				index, status := catalog.AttributeIndex()
				return index, routing.ModelAttributeSnapshot{Version: status.Version, LastUpdated: status.LastUpdated, LastError: status.LastError}
			},
			Lookup:     catalog.ModelAttributes,
			Update:     catalog.ForceUpdate,
			Candidates: modelidentity.CandidatesFactory,
			Snapshot: func() routing.ModelAttributeSnapshot {
				snapshot := catalog.AttributesSnapshot()
				return routing.ModelAttributeSnapshot{Items: snapshot.Items, Version: snapshot.Version, LastUpdated: snapshot.LastUpdated, LastError: snapshot.LastError}
			},
		},
	}
}

// provideModelCatalogService extracts catalogue options from bootstrap configuration.
// ModelCatalogInitialization and ModelCatalogService hooks manage initialization and periodic updates,
// respectively.
func provideModelCatalogService(cfg *config.Config, remote provider.RemoteClient) (*provider.Service, error) {
	options := provider.Options{
		DataDir:               cfg.Pricing.DataDir,
		RemoteURL:             cfg.Pricing.RemoteURL,
		FallbackFile:          cfg.Pricing.FallbackFile,
		CheckIntervalMinutes:  cfg.Pricing.CheckIntervalMinutes,
		URLAllowlistEnabled:   cfg.Security.URLAllowlist.Enabled,
		AllowInsecureHTTP:     cfg.Security.URLAllowlist.AllowInsecureHTTP,
		AllowPrivateHosts:     cfg.Security.URLAllowlist.AllowPrivateHosts,
		PricingHosts:          slices.Clone(cfg.Security.URLAllowlist.PricingHosts),
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}
	return provider.NewService(options, remote), nil
}

// provideModelCatalogRemoteClient configures the update proxy and direct-connection fallback.
func provideModelCatalogRemoteClient(cfg *config.Config) provider.RemoteClient {
	return provider.NewRemoteClient(cfg.Update.ProxyURL, cfg.Security.ProxyFallback.AllowDirectOnError)
}
