//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// assembly_ops_wire.go registers system maintenance, Ops monitoring, and
// logging dependencies with Wire.
// System maintenance obtains a shared operation lock, while Ops receives
// runtime failures through its error queue and system log sink. Wire supplies
// the maintenance HTTP entry point and monitoring constructors from the same
// application resources.

// opsAssemblyProviders supplies system maintenance, operational monitoring,
// and logging dependencies through the application Ops provider set.
var opsAssemblyProviders = wire.NewSet(
	provideSystemLock,
	provideSystemOperations,
	provideSystemHTTP,
	provideOpsErrorQueue,
	opsProviders,
	provideSystemLogSink,
)
