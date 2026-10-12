//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Creative and batch image handlers participate in task activity tracking, so
// shutdown can wait for submissions, downloads, and management calls. Their
// workers and public services use application-assembled pricing, funding, and
// task storage dependencies.

// tasksAssemblyProviders supplies creative and batch-image HTTP, worker,
// download, cleanup, activity, and runtime-settings constructors for the
// application.
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
