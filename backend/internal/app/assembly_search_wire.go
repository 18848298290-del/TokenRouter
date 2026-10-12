//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Configuration loading publishes the active search Manager through a shared
// Registry. The HTTP handler manages that ConfigService, and lifecycle
// registration initializes it before the server accepts requests and waits
// for in-flight searches and quota cleanup during shutdown.

// searchAssemblyProviders supplies the search registry, execution runtime, and
// HTTP handler constructors consumed by initializeApplication.
var searchAssemblyProviders = wire.NewSet(
	provideSearchRegistry,
	provideSearchRuntime,
	provideSearchHTTP,
)
