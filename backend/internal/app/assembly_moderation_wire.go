//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// moderationAssemblyProviders groups Wire providers for the moderation module.
var moderationAssemblyProviders = wire.NewSet(
	provideModerationStore,
	provideModerationHashes,
	provideModerationCore,
	provideModerationHTTP,
	provideModerationSettings,
)
