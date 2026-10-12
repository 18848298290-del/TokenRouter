package app

import (
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	egresshttp "github.com/TokenFlux/TokenRouter/internal/egress/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	opshttp "github.com/TokenFlux/TokenRouter/internal/ops/httpapi"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	routinghttp "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	usagehttp "github.com/TokenFlux/TokenRouter/internal/usage/httpapi/admin"
)

type idempotencyHTTPReady struct{}

// provideIdempotencyHTTP binds all idempotent endpoints to the application's coordinator before HTTP starts.
// Each application instance has its own coordinator binding.
func provideIdempotencyHTTP(
	coordinator *idempotency.IdempotencyCoordinator,
	providers *providerhttp.ManagementHandler,
	archive *providerhttp.ArchiveHandler,
	codex *providerhttp.CodexImportHandler,
	keys *keyhttp.APIKeyHandler[routingdto.Group],
	redeem *billinghttp.AdminRedeemHandler,
	subscriptions *billinghttp.AdminSubscriptionHandler,
	proxies *egresshttp.ProxyHandler,
	users *identityhttp.AdminUserHandler[keydto.APIKey[routingdto.Group]],
	groups *routinghttp.GroupHandler,
	system *opshttp.SystemHandler,
	usage *usagehttp.UsageHandler,
) *idempotencyHTTPReady {
	for _, consumer := range []interface {
		BindIdempotency(*idempotency.IdempotencyCoordinator)
	}{providers, archive, codex, keys, redeem, subscriptions, proxies, users, groups, system, usage} {
		consumer.BindIdempotency(coordinator)
	}
	return &idempotencyHTTPReady{}
}
