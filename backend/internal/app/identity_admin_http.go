package app

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// identity_admin_http.go assembles user and Key administration and adapts their
// data for HTTP responses. User administration needs Key display data and live
// concurrency alongside identity mutations. The application binds Key and user
// transaction participants to the shared Ent client, and the HTTP adapter
// converts Key group display and concurrency loads for administrator responses.

// identityAdminKeys adapts stored Key pages for identity administration
// responses.
type identityAdminKeys struct {
	*keypostgres.KeyStore // shared Key storage
}

// List returns Key summaries using the requested page, size, and ordering. Nil
// Key results stay nil, absent pagination yields total zero, and GroupID
// references are passed from the stored Key rows.
func (p identityAdminKeys) List(ctx context.Context, id int64, page, size int, sortBy, order string) ([]identity.AdminKeySummary, int64, error) {
	keys, pagination, e := p.ListByUserID(ctx, id, pagination.PaginationParams{Page: page, PageSize: size, SortBy: sortBy, SortOrder: order}, apikey.APIKeyListFilters{})
	if e != nil {
		return nil, 0, e
	}
	var out []identity.AdminKeySummary
	if keys != nil {
		out = make([]identity.AdminKeySummary, len(keys))
		for i, k := range keys {
			out[i] = identity.AdminKeySummary{ID: k.ID, Key: k.Key, GroupID: k.GroupID}
		}
	}
	if pagination == nil {
		return out, 0, nil
	}
	return out, pagination.Total, nil
}

// provideIdentityAdmin binds user administration to shared entitlement, cache,
// and Key services. Key lifecycle writes receive the selected Ent transaction,
// and tasks supplies tracked post-mutation background work for application
// shutdown.
func provideIdentityAdmin(client *dbent.Client, users *identitypostgres.UserStore, keys *keypostgres.KeyStore, groups *routingpostgres.GroupStore, rates billing.UserGroupRateRepository, rpm scheduler.UserRPMCache, settings *identityAuthSettings, subs identity.DefaultSubscriptionAssigner, balances billing.BalanceAdjuster, records *billing.RedeemAdmin, invalidator apikey.APIKeyAuthCacheInvalidator, cache *billing.Eligibility, affiliates *promotion.AffiliateService, tasks *lifecycle.Tasks) *identity.UserAdmin {
	observe := identity.Observer{Log: logging.LegacyPrintf}
	transactions := &identitypostgres.AdminMutations{Client: client, Users: users, Keys: keys, KeysInTx: func(tx *dbent.Tx) identity.AdminKeyParticipant { return keys.LifecycleInTx(tx) }, Observer: observe}
	return identity.NewUserAdmin(identity.AdminDependencies{Now: time.Now, Users: users, Groups: identityAdminGroups{Repository: groups}, Keys: identityAdminKeys{keys}, Rates: rates, RPM: rpm, Settings: settings, Subscriptions: subs, Balances: balances, Records: records, Invalidator: invalidator, BalanceCache: cache, Affiliates: affiliates, Transactions: transactions, Observer: observe, Background: tasks.Go})
}

// provideKeyAdmin binds Key group mutation and user access participants to
// shared stores. UsersInTx creates the access writer from the selected Ent
// transaction, while invalidator and billing cache supply later administration
// effects.
func provideKeyAdmin(client *dbent.Client, keys *keypostgres.KeyStore, users *identitypostgres.UserStore, groups *routingpostgres.GroupStore, invalidator apikey.APIKeyAuthCacheInvalidator, cache *billing.Eligibility) *apikey.Admin {
	mutations := &keypostgres.AdminGroupMutations{Client: client, Keys: keys, Users: users, UsersInTx: func(tx *dbent.Tx) keypostgres.GroupAccessWriter { return identitypostgres.GroupAccessInTx(tx) }, Observer: logging.LegacyPrintf}
	return &apikey.Admin{Keys: keys, Users: users, Groups: keyGroups{Repository: groups}, Mutations: mutations, Invalidator: invalidator, RateLimits: cache}
}

// provideIdentityAdminHTTP supplies Key DTO reads, concurrency loads, and
// step-up enforcement to user HTTP. The load callback returns available counts
// alongside the scheduler error, omitting nil load entries from its result map.
func provideIdentityAdminHTTP(admin *identity.UserAdmin, keys *apikey.Admin, concurrency *scheduler.ConcurrencyService, totp *identity.TotpService, users *identity.UserService, settings *identity.RuntimeSettings) *identityhttp.AdminUserHandler[keydto.APIKey[routingdto.Group]] {
	listKeys := func(ctx context.Context, id int64, page, size int, sortBy, order string) ([]keydto.APIKey[routingdto.Group], int64, error) {
		rows, total, err := keys.GetUserAPIKeys(ctx, id, page, size, sortBy, order)
		if err != nil {
			return nil, 0, err
		}
		result := make([]keydto.APIKey[routingdto.Group], 0, len(rows))
		for i := range rows {
			result = append(result, *keydto.APIKeyFromKey(&rows[i], func(group *routing.Group) *routingdto.Group {
				return routingdto.GroupFromRouting(apikey.RoutingGroup(group))
			}))
		}
		return result, total, nil
	}
	readConcurrency := func(ctx context.Context, users []identity.User) (map[int64]int, error) {
		views := make([]scheduler.UserWithConcurrency, len(users))
		for i, user := range users {
			views[i] = scheduler.UserWithConcurrency{ID: user.ID, MaxConcurrency: user.Concurrency}
		}
		loads, err := concurrency.GetUsersLoadBatch(ctx, views)
		result := make(map[int64]int, len(loads))
		for id, load := range loads {
			if load != nil {
				result[id] = load.CurrentConcurrency
			}
		}
		return result, err
	}
	return identityhttp.NewAdminUserHandler(admin, listKeys, readConcurrency, func(c *gin.Context) bool {
		return identityhttp.EnforceStepUp(c, totp, users, settings)
	})
}
