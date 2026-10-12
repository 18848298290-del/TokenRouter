//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// tasksAssemblyProviders groups Wire providers for the tasks module.
var tasksAssemblyProviders = wire.NewSet(
	provideTaskActivity,
	provideCreativePublic,
	provideCreativeWorkerRuntime,
	provideBatchPricing,
	provideBatchPublic,
	provideBatchDownload,
	provideBatchCleanup,
	provideBatchCleanupRuntime,
	provideBatchRuntime,
	provideBatchRegistry,
	provideCreativeHTTP,
	provideBatchHTTP,
	provideCreativeRuntimeSettings,
	provideCreativeSettingsHTTP,
)
