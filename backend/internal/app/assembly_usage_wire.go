//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// usageAssemblyProviders groups Wire providers for the usage module.
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
