//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// opsAssemblyProviders groups Wire providers for the ops module.
var opsAssemblyProviders = wire.NewSet(
	provideSystemLock,
	provideSystemOperations,
	provideSystemHTTP,
	provideOpsErrorQueue,
	opsProviders,
	provideSystemLogSink,
)
