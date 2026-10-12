//go:build wireinject

package app

import (
	"github.com/google/wire"

	teamredis "github.com/TokenFlux/TokenRouter/internal/team/rediscache"
)

// teamAssemblyProviders groups Wire providers for the team module.
var teamAssemblyProviders = wire.NewSet(
	teamHTTPProviders,
	provideTeam,
	provideTeamRepository,
	teamredis.NewTeamInvitationLimiter,
)
