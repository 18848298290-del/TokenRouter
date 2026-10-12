//go:build wireinject

package app

import "github.com/google/wire"

// gatewaySearchProviders groups constructors for search tools and HTTP endpoints.
var gatewaySearchProviders = wire.NewSet(ProvideGatewaySearchTools, ProvideGatewaySearchHTTP, provideGrokSearchExecutor)
