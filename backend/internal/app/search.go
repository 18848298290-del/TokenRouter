package app

import (
	"context"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	egresspg "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	"github.com/TokenFlux/TokenRouter/internal/search"
	searchhttp "github.com/TokenFlux/TokenRouter/internal/search/httpapi"
	searchadapter "github.com/TokenFlux/TokenRouter/internal/search/provider"
	"github.com/TokenFlux/TokenRouter/internal/search/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// search.go assembles search configuration, shared registry, proxy lookup, and
// optional Redis quota tracking. The application initializes configuration
// before HTTP startup and asks the runtime to wait for in-flight searches and
// quota cleanup during shutdown; handlers and gateway consumers share its
// registry.

// searchProxies adapts the proxy store to search configuration URL lookup.
type searchProxies struct {
	proxies *egresspg.ProxyStore /* persisted proxy URL reader */
}

// provideSearchRegistry creates the registry shared by configuration and search execution.
func provideSearchRegistry() *search.Registry {
	registry := search.NewRegistry()
	return registry
}

// provideSearchRuntime constructs search configuration loading and registers
// its initialization and shutdown hooks. A nil Redis client leaves quota state
// unset; the runtime and its executors share the supplied registry.
func provideSearchRuntime(store *settings.Store, proxies *egresspg.ProxyStore, r *redis.Client, registry *search.Registry, manager *lifecycle.Manager) *search.ConfigService {
	var state search.QuotaState
	if r != nil {
		state = rediscache.New(r)
	}
	runtime := search.NewConfigService(store, searchProxies{proxies}, func(configs []search.ProviderConfig, work *search.WorkGroup) *search.Manager {
		return search.NewManager(configs, state, searchadapter.NewExecutor(), work)
	}, registry)
	manager.Register(lifecycle.Hook{Name: "WebSearchRuntime", StartOrder: 181, StopOrder: shutdownStageAggregatesSessionsAndSearch, Start: runtime.Initialize, Stop: runtime.StopContext})
	return runtime
}

// provideSearchHTTP creates the HTTP adapter for the assembled search configuration service.
func provideSearchHTTP(runtime *search.ConfigService) *searchhttp.Handler {
	return searchhttp.New(runtime)
}

// URLs reads persisted proxies and returns their addresses indexed by proxy ID.
// Missing IDs have no map entry, allowing ConfigService to skip providers whose
// configured proxy cannot be resolved.
func (s searchProxies) URLs(ctx context.Context, ids []int64) (map[int64]string, error) {
	values, err := s.proxies.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(values))
	for _, v := range values {
		out[v.ID] = v.URL()
	}
	return out, nil
}
