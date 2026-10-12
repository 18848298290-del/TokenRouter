package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// WithManagedRefresh rereads state under shared refresh locking and runs the administrator callback; each
// write commits separately.
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

// ValidateManagedRefreshTarget accepts OAuth, Setup Token, and Qoder COSY providers, rejecting credential
// shadows before exchange.
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
