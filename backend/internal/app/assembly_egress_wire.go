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

// egressAssemblyProviders groups Wire providers for the egress module.
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
