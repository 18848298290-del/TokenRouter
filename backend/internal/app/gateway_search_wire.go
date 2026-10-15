//go:build wireinject

package app

import "github.com/google/wire"

// gateway_search_wire.go registers search-tool emulation, standalone search
// HTTP, and Grok execution constructors with Wire.
// Emulated search tools use the search configuration service and pricing
// rules, while standalone search binds Grok selection, funding, moderation,
// and completion recording. The gateway graph resolves both entry paths
// through their constructors and the Grok executor used by the standalone
// handler.

// gatewaySearchProviders supplies tool emulation, standalone search HTTP, and
// the Grok executor used by standalone search.
var gatewaySearchProviders = wire.NewSet(ProvideGatewaySearchTools, ProvideGatewaySearchHTTP, provideGrokSearchExecutor)
