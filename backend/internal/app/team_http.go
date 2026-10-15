//go:build wireinject

package app

import (
	"github.com/google/wire"

	teamhttp "github.com/TokenFlux/TokenRouter/internal/team/httpapi"
)

// team_http.go registers user and administrator team HTTP constructors with
// Wire.
// The application resolves the user and administrator handlers from their typed
// constructor dependencies before passing those assembled instances to the
// corresponding panel route registrars.

// teamHTTPProviders supplies user and administrator team handler constructors
// to the application Wire graph.
var teamHTTPProviders = wire.NewSet(teamhttp.NewUserHandler, teamhttp.NewAdminHandler)
