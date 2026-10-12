//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/site"
	sitepostgres "github.com/TokenFlux/TokenRouter/internal/site/postgres"
)

// siteAssemblyProviders groups Wire providers for the site module.
var siteAssemblyProviders = wire.NewSet(
	provideSiteDisplay,
	provideSitePublicHTTP,
	provideSitePublic,
	provideSitePages,
	site.NewAnnouncementService,
	sitepostgres.NewAnnouncementRepository,
	sitepostgres.NewAnnouncementReadRepository,
	provideAnnouncementUsers,
	provideAnnouncementSubscriptions,
	provideAnnouncementExpiry,
)
