//go:build wireinject

package app

import (
	"github.com/google/wire"

	teamhttp "github.com/TokenFlux/TokenRouter/internal/team/httpapi"
)

// teamHTTPProviders groups user and admin team HTTP constructors.
var teamHTTPProviders = wire.NewSet(teamhttp.NewUserHandler, teamhttp.NewAdminHandler)
