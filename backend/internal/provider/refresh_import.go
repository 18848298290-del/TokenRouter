package provider

import (
	"context"
	"errors"
	"fmt"
)

// RefreshImported attempts one exchange for a persisted import identity using the shared locks and stop lifecycle.
// The import flow decides eligibility, including disabled providers; returned credential metadata is persisted
// as supplied.
func (api *OAuthRefreshAPI) RefreshImported(ctx context.Context, value *Record, key string, exchange func(context.Context, *Record) map[string]any) error {
	if value == nil || value.IsCredentialShadow() || exchange == nil {
		return nil
	}
	if api == nil || api.providerRepo == nil {
		return errors.New("oauth refresh provider repository is not configured")
	}
	ctx, finish, err := api.beginRefresh(ctx)
	if err != nil {
		return err
	}
	defer finish()
	release, held, err := api.acquireRefreshLock(ctx, value.ID, key)
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	defer release()
	current, err := api.providerRepo.GetByID(ctx, value.ID)
	if err != nil {
		return err
	}
	if current == nil || current.ID != value.ID || current.Status != value.Status || RefreshCredentialIdentity(current) != RefreshCredentialIdentity(value) {
		return ErrRefreshProviderStateChanged
	}
	attempted := snapshotRefreshRecord(current)
	if err := ctx.Err(); err != nil {
		return err
	}
	credentials := exchange(ctx, current)
	if err := ctx.Err(); err != nil {
		return err
	}
	if credentials == nil {
		return nil
	}
	writer, ok := api.providerRepo.(CredentialRefreshWriter)
	if !ok {
		return fmt.Errorf("%w: conditional credential writer is not configured", ErrRefreshCredentialPersist)
	}
	applied, err := writer.UpdateOAuthCredentialsIfUnchanged(ctx, CredentialVersion{ID: attempted.ID, Platform: attempted.Platform, Type: attempted.Type, Status: attempted.Status, ProxyID: attempted.ProxyID, Credentials: attempted.Credentials}, CloneValues(credentials))
	if err != nil {
		return err
	}
	if !applied {
		// A comparison miss discards the exchange result; reread to confirm that the provider still exists.
		latest, readErr := api.providerRepo.GetByID(ctx, value.ID)
		if readErr != nil {
			return readErr
		}
		if latest == nil || latest.ID != value.ID {
			return ErrRefreshProviderStateChanged
		}
		return nil
	}
	return nil
}
