package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// ops_projections.go adapts provider and user records into the views consumed
// by operational queries. Ops observes a narrowed provider and user view rather
// than request execution credentials. Provider conversion includes health
// deadlines and lightweight group display, then clones that result before
// returning; user conversion keeps the identity and concurrency fields needed
// by reports and sampling.

// opsProviderReader adapts shared provider queries into operations observation
// data.
type opsProviderReader struct {
	store *providerpostgres.ProviderStore // provider observation storage
}

// opsUsers adapts identity user reads for Ops reports and sampling.
type opsUsers struct {
	store *identitypostgres.UserStore // identity observation storage
}

// ListPage converts filtered provider rows and retains the repository
// pagination result.
func (a opsProviderReader) ListPage(ctx context.Context, p pagination.PaginationParams, platform string, group int64) ([]ops.ProviderObservation, *pagination.PaginationResult, error) {
	v, pg, e := a.store.ListWithFilters(ctx, p, platform, "", "", "", group, "")
	return opsProviderViews(v), pg, e
}

// ListOpsProvidersForStats converts the repository's scoped provider rows for
// Ops statistics.
func (a opsProviderReader) ListOpsProvidersForStats(ctx context.Context, platform string, g *int64) ([]ops.ProviderObservation, error) {
	v, e := a.store.ListOpsProvidersForStats(ctx, platform, g)
	return opsProviderViews(v), e
}

// ListSchedulable returns narrowed observations of the store's current
// scheduling candidates.
func (a opsProviderReader) ListSchedulable(ctx context.Context) ([]ops.ProviderObservation, error) {
	v, e := a.store.ListSchedulable(ctx)
	return opsProviderViews(v), e
}

// ListSchedulableProviderLoads adapts provider IDs and capacity limits for Ops
// concurrency reads.
func (a opsProviderReader) ListSchedulableProviderLoads(ctx context.Context) ([]ops.ProviderWithConcurrency, error) {
	v, e := a.store.ListSchedulableProviderLoads(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]ops.ProviderWithConcurrency, len(v))
	for i, a := range v {
		out[i] = ops.ProviderWithConcurrency{ID: a.ID, MaxConcurrency: a.MaxConcurrency}
	}
	return out, nil
}

// opsProviderViews selects display, load, and health fields and clones the
// resulting observations. It retains nil group elements, creates group display
// entries for nonnil elements, and returns an empty outer slice for absent
// input.
func opsProviderViews(a []provider.Record) []ops.ProviderObservation {
	out := make([]ops.ProviderObservation, len(a))
	for i, v := range a {
		out[i] = ops.ProviderObservation{ID: v.ID, Name: v.Name, Platform: v.Platform, Status: v.Status, ErrorMessage: v.ErrorMessage, Schedulable: v.Schedulable, Concurrency: v.Concurrency, LoadFactor: v.EffectiveLoadFactor(), TempUnschedulableUntil: v.TempUnschedulableUntil, RateLimitResetAt: v.RateLimitResetAt, OverloadUntil: v.OverloadUntil}
		if v.Groups != nil {
			out[i].Groups = make([]*ops.GroupObservation, len(v.Groups))
			for j, g := range v.Groups {
				if g != nil {
					out[i].Groups[j] = &ops.GroupObservation{ID: g.ID, Name: g.Name}
				}
			}
		}
	}
	return querycache.Clone(out)
}

// ListActivePage queries active identity users and returns narrowed Ops
// observations.
func (a opsUsers) ListActivePage(ctx context.Context, p pagination.PaginationParams) ([]ops.UserObservation, *pagination.PaginationResult, error) {
	v, pg, e := a.store.ListWithFilters(ctx, p, identity.UserListFilters{Status: "active"})
	out := make([]ops.UserObservation, len(v))
	for i, u := range v {
		out[i] = ops.UserObservation{ID: u.ID, Email: u.Email, Username: u.Username, Concurrency: u.Concurrency}
	}
	return out, pg, e
}

// GetFirstAdmin returns identity and concurrency data for the stored
// administrator. An absent user remains nil, and an available user is converted
// alongside any repository error for the report caller.
func (a opsUsers) GetFirstAdmin(ctx context.Context) (*ops.UserObservation, error) {
	u, e := a.store.GetFirstAdmin(ctx)
	if u == nil {
		return nil, e
	}
	return &ops.UserObservation{ID: u.ID, Email: u.Email, Username: u.Username, Concurrency: u.Concurrency}, e
}
