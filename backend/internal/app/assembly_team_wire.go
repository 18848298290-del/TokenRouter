//go:build wireinject

package app

import (
	"github.com/google/wire"

	teamredis "github.com/TokenFlux/TokenRouter/internal/team/rediscache"
)

// Team membership, member usage, and API key lifecycle changes share the
// application SQL pool through the team repository. Wire passes that repository
// and a Redis invitation limiter into the team service used by HTTP handlers.

// teamAssemblyProviders supplies team HTTP adapters, team services, persistent
// storage, and invitation limiting to the application graph.
var teamAssemblyProviders = wire.NewSet(
	teamHTTPProviders,
	provideTeam,
	provideTeamRepository,
	teamredis.NewTeamInvitationLimiter,
)
