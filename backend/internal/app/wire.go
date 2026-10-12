//go:build wireinject

package app

import (
	"context"

	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
)

// initializeApplication constructs and registers resources; Application.Run starts them.
func initializeApplication(ctx context.Context, cfg *config.Config, info BuildInfo, manager *lifecycle.Manager, restarter *lifecycle.Restarter, tasks *lifecycle.Tasks) (*Application, error) {
	wire.Build(
		egressAssemblyProviders,
		providerAssemblyProviders,
		gatewayAssemblyProviders,
		identityAssemblyProviders,
		apikeyAssemblyProviders,
		siteAssemblyProviders,
		routingAssemblyProviders,
		foundationAssemblyProviders,
		teamAssemblyProviders,
		backupAssemblyProviders,
		opsAssemblyProviders,
		tasksAssemblyProviders,
		settingsAssemblyProviders,
		paymentAssemblyProviders,
		promotionAssemblyProviders,
		searchAssemblyProviders,
		moderationAssemblyProviders,
		notificationAssemblyProviders,
		modelCatalogAssemblyProviders,
		billingAssemblyProviders,
		usageAssemblyProviders,
		auditAssemblyProviders,
		schedulerAssemblyProviders,
		upstreamAssemblyProviders,
		httpAssemblyProviders,
		runtimeAssemblyProviders,
	)
	return nil, nil
}
