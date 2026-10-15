package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// refresh_managed.go coordinates administrator refresh callbacks against
// current provider identity under shared refresh locks.
// An administrator can refresh disabled OAuth, Setup Token or Qoder COSY
// credentials, while Spark shadows delegate authentication to their parent. The
// management entry point validates that provider role before registering work,
// then shares the ordinary coordinator's refresh key and shutdown lifecycle.
//
// A locked reread compares ID, platform and type with selection before the
// callback sees current credentials. The callback receives a CloneRecord
// snapshot and owns its persistence sequence, whose writes can commit
// separately. Distributed contention instead returns a copied current record
// with an empty warning. A status or credential change can remain eligible for
// this administrative path, leaving conditional write checks to the callback
// rather than applying background expiry rules.

// WithManagedRefresh coordinates the administrative callback with shared
// refresh protection and a reread of its current provider target. Reached
// work requires a nonnil context, consistent key and callable apply; validation
// rejects missing or unsupported targets before registration.
//
// The reread compares ID, platform and type, then repeats role validation
// without requiring active status or expiry. Reported distributed contention
// skips apply and returns the copied current record with an empty warning;
// Redis retries can report that result after this call installed a lease but
// lost its reply.
// Otherwise callback results and warnings pass through while the coordinator
// retains locks and activity registration until callback completion. Its
// independent writes keep their own commit and rollback behavior, and
// unsupported mutable CloneRecord values still require stability from their
// owners.
func (api *OAuthRefreshAPI) WithManagedRefresh(ctx context.Context, observed *Record, key string, apply func(context.Context, *Record) (*Record, string, error)) (*Record, string, error) {
	if err := ValidateManagedRefreshTarget(observed); err != nil {
		return nil, "", err
	}
	if api == nil || api.providerRepo == nil {
		return nil, "", errors.New("oauth refresh provider repository is not configured")
	}
	if observed == nil {
		return nil, "", ErrProviderNotFound
	}
	ctx, finish, err := api.beginRefresh(ctx)
	if err != nil {
		return nil, "", err
	}
	defer finish()
	release, held, err := api.acquireRefreshLock(ctx, observed.ID, key)
	if err != nil {
		return nil, "", err
	}
	if !held {
		defer release()
	}
	// Use the latest credentials after lock selection so administrative refresh
	// does not exchange the earlier selected token while a prior owner has
	// rotated it.
	current, err := api.providerRepo.GetByID(ctx, observed.ID)
	if err != nil {
		return nil, "", err
	}
	if current == nil || current.ID != observed.ID || current.Platform != observed.Platform || current.Type != observed.Type {
		return nil, "", ErrRefreshProviderStateChanged
	}
	if err := ValidateManagedRefreshTarget(current); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if held {
		return CloneRecord(current), "", nil
	}
	return apply(ctx, CloneRecord(current))
}

// ValidateManagedRefreshTarget accepts OAuth, Setup Token and Qoder COSY records
// for administrative refresh. Missing records return ErrProviderNotFound, other
// credential kinds return NOT_OAUTH, and Spark shadows return their dedicated
// rejection before exchange.
func ValidateManagedRefreshTarget(value *Record) error {
	if value == nil {
		return ErrProviderNotFound
	}
	if !value.IsOAuth() && !value.IsQoderCosy() {
		return apperror.BadRequest("NOT_OAUTH", "cannot refresh non-OAuth provider")
	}
	if value.IsCredentialShadow() {
		return apperror.BadRequest("SPARK_SHADOW_NO_REFRESH", "cannot refresh spark shadow provider; its credentials are managed by the parent provider")
	}
	return nil
}
