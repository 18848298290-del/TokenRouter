package app

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// provideModelAttributes 为管理和展示接口提供模型属性服务。
func provideModelAttributes(repo *postgres.ModelAttributeStore, catalog *provider.Service, invalidator apikey.APIKeyAuthCacheInvalidator, catalogue *routing.RequestableCatalogue) *routing.ModelAttributeService {
	return &routing.ModelAttributeService{
		Repo:        repo,
		Invalidator: invalidator,
		Catalogue:   catalogue,
		Catalog: routing.ModelAttributeCatalog{
			View:       catalog.CapabilityView,
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

// provideModelCatalogService 从 bootstrap 配置提取模型目录服务的参数。
// ModelCatalogInitialization 和 ModelCatalogService hook 分别负责初始化及周期更新的启停。
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

// provideModelCatalogRemoteClient 配置更新代理和直连回退开关。
func provideModelCatalogRemoteClient(cfg *config.Config) provider.RemoteClient {
	return provider.NewRemoteClient(cfg.Update.ProxyURL, cfg.Security.ProxyFallback.AllowDirectOnError)
}
