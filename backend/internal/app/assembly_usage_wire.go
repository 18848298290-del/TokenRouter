//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Usage records feed user reports, administrator reports, and cached
// dashboards. The application passes shared aggregation settings into
// dashboard and background aggregation constructors, so those consumers make
// decisions from the same preaggregation configuration.

// usageAssemblyProviders supplies usage reporting, dashboards, storage,
// aggregation, cleanup, and HTTP constructors to the application graph.
var usageAssemblyProviders = wire.NewSet(
	provideUsageDashboardCache,
	providePublicUsage,
	provideUsageKeys,
	provideUsageUsers,
	provideUsageHTTP,
	provideUsageSettings,
	provideAdminUsageHTTP,
	provideDashboardHTTP,
	provideUsageOptions,
	provideUsageStore,
	provideUsageRepository,
	provideUsageService,
	provideUsageAggregationRepository,
	provideUsageCleanupRepository,
	provideUsageAggregation,
	provideUsageCleanup,
	provideUsageDashboard,
)
