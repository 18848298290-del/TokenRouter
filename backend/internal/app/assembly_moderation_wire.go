//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Gateway moderation and its administrator routes need the same assembled core
// and settings reader. Persistence and content hashes join those dependencies
// in Wire, so application consumers resolve moderation services through the
// same module provider set.

// moderationAssemblyProviders supplies moderation persistence, hashes, core
// decisions, settings, and HTTP administration to initializeApplication.
var moderationAssemblyProviders = wire.NewSet(
	provideModerationStore,
	provideModerationHashes,
	provideModerationCore,
	provideModerationHTTP,
	provideModerationSettings,
)
