//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// settingsAssemblyProviders groups Wire providers for the settings module.
var settingsAssemblyProviders = wire.NewSet(
	provideCompositeReadOptions,
	providePreAggregationSettings,
	provideCompositeSettingsHTTP,
	provideCompositeRuntime,
	provideSettingsParticipants,
	providePreAggregationHTTP,
)
