//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/server"
	serverhttp "github.com/TokenFlux/TokenRouter/internal/server/httpapi"
)

// httpAssemblyProviders groups HTTP server and route Wire providers.
var httpAssemblyProviders = wire.NewSet(
	nativeHTTPProviders,
	server.ProviderSet,
	provideHTTPOptions,
	provideHTTPRouteMount,
	provideAuthRouteMount,
	provideUserRouteMount,
	provideAdminRouteMount,
	provideGatewayRouteMount,
	providePaymentRouteMount,
	provideForwardedSettings,
	providePanelSettings,
	serverhttp.NewPanelSettingsHandler,
	provideRouterRuntime,
	provideRequestLog,
)
