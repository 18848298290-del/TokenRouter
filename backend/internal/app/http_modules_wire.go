//go:build wireinject

package app

import (
	"github.com/google/wire"

	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	opshttp "github.com/TokenFlux/TokenRouter/internal/ops/httpapi"
	sitehttp "github.com/TokenFlux/TokenRouter/internal/site/httpapi"
)

// nativeHTTPProviders groups HTTP constructors that share application use cases.
var nativeHTTPProviders = wire.NewSet(
	billinghttp.NewPlanHandler,
	billinghttp.NewRedeemHandler,
	billinghttp.NewSubscriptionHandler,
	billinghttp.NewAdminRedeemHandler,
	billinghttp.NewAdminSubscriptionHandler,
	sitehttp.NewAnnouncementHandler,
	sitehttp.NewAdminAnnouncementHandler,
	opshttp.NewOpsHandler,
)
