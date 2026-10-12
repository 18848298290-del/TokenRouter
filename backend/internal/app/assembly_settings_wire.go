//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// The combined settings page reads values owned by registered module
// participants and applies them through a composite runtime. Wire supplies
// participant registration before handler construction, alongside
// preaggregation settings that connect administration to usage and Ops
// aggregation.

// settingsAssemblyProviders supplies composite settings readers, runtime
// participants, and pre-aggregation HTTP and settings dependencies for
// application administration.
var settingsAssemblyProviders = wire.NewSet(
	provideCompositeReadOptions,
	providePreAggregationSettings,
	provideCompositeSettingsHTTP,
	provideCompositeRuntime,
	provideSettingsParticipants,
	providePreAggregationHTTP,
)
