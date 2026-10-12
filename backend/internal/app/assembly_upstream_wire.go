//go:build wireinject

package app

import (
	"github.com/google/wire"

	gatewaytransport "github.com/TokenFlux/TokenRouter/internal/gateway/provider/transport"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	anthropicredis "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic/rediscache"
)

// upstreamAssemblyProviders groups upstream client Wire providers.
var upstreamAssemblyProviders = wire.NewSet(
	wire.Bind(new(httpclient.UpstreamTransport), new(*gatewaytransport.Client)),
	upstreamClientProviders,
	providePrivacyClientFactory,
	anthropicredis.NewFingerprintStore,
)
