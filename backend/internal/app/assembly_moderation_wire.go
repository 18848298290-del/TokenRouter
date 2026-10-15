//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// assembly_moderation_wire.go registers moderation persistence, content
// hashes, decisions, settings, and HTTP administration with Wire.
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
