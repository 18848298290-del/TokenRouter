//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// searchAssemblyProviders groups Wire providers for the search module.
var searchAssemblyProviders = wire.NewSet(
	provideSearchRegistry,
	provideSearchRuntime,
	provideSearchHTTP,
)
