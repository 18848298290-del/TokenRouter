//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egresshttp "github.com/TokenFlux/TokenRouter/internal/egress/httpapi"
	egresspostgres "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	"github.com/TokenFlux/TokenRouter/internal/egress/provider"
	egressredis "github.com/TokenFlux/TokenRouter/internal/egress/rediscache"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// assembly_egress_wire.go registers proxy and TLS constructors and binds TLS
// readers for provider token requests.
// OpenAI token requests consult configured TLS profile and router readers.
// Wire binds those reader interfaces to the egress implementations, while
// proxy administration receives its own persisted proxy store, probe adapter,
// and Redis latency cache.

// egressAssemblyProviders supplies proxy and TLS services, including the TLS
// readers used by provider token adapters.
var egressAssemblyProviders = wire.NewSet(
	wire.Bind(new(provideradapter.OpenAITokenProfileResolver), new(*provider.TLSProfiles)),
	wire.Bind(new(provideradapter.OpenAITokenRouterReader), new(*egress.TLSFingerprintRouterService)),
	provideProxyExpiry,
	provideProxyTransfer,
	provideProxyHTTP,
	provideEgressProxyStore,
	wire.Bind(new(egress.ProxyRepository), new(*egresspostgres.ProxyStore)),
	provideEgressProbe,
	provideEgressAdmin,
	egressredis.NewProxyLatencyCache,
	egresspostgres.NewTLSFingerprintProfileRepository,
	egresspostgres.NewTLSFingerprintRouterRepository,
	egressredis.NewTLSFingerprintProfileCache,
	egressredis.NewTLSFingerprintRouterCache,
	provideEgressProfiles,
	provider.NewTLSProfiles,
	provideEgressRouters,
	provideEgressCollector,
	provideEgressProfileHTTP,
	egresshttp.NewTLSFingerprintRouterHandler,
)
