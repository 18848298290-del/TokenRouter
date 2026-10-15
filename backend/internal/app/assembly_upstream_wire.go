//go:build wireinject

package app

import (
	"github.com/google/wire"

	gatewaytransport "github.com/TokenFlux/TokenRouter/internal/gateway/provider/transport"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	anthropicredis "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic/rediscache"
)

// assembly_upstream_wire.go registers upstream clients, privacy clients, and
// fingerprint storage and binds the shared gateway transport.
// Upstream requests use a transport assembled from gateway policy and platform
// clients. Wire binds that transport for the shared HTTP client pool and adds
// privacy clients and Anthropic fingerprint storage, making the execution
// dependencies available across platform adapters.

// upstreamAssemblyProviders supplies platform clients and binds the gateway
// transport used by the shared upstream HTTP pool.
var upstreamAssemblyProviders = wire.NewSet(
	wire.Bind(new(httpclient.UpstreamTransport), new(*gatewaytransport.Client)),
	upstreamClientProviders,
	providePrivacyClientFactory,
	anthropicredis.NewFingerprintStore,
)
