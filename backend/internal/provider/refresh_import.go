package provider

import (
	"context"
	"errors"
	"fmt"
)

// refresh_import.go refreshes imported credentials under shared locks and
// conditional persistence checks.
// Imported credentials can retain an explicitly disabled or errored provider
// status and supplied token-version metadata. The import entry point therefore
// uses its selected persisted identity, shared refresh locks and shutdown
// tracking while leaving eligibility choices to the import flow.
//
// Under the lock it rereads ID, status and authentication identity before one
// exchange, then snapshots the attempted record for conditional persistence.
// Cancellation observed after exchange discards its returned credentials. A
// comparison miss rereads existence and treats a surviving updated provider as
// handled; it does not repeat the exchange or overwrite the newer identity. The
// returned map is copied through CloneValues and saved with its metadata as
// supplied.

// RefreshImported exchanges once for an imported persisted identity and
// conditionally saves the returned credentials. Nil record, credential shadow or
// nil exchange is a successful skip; reached registration needs a nonnil context
// and consistent refresh lock key.
//
// The selected record stays stable through the locked reread and identity
// comparison. Credential values need to be JSON-encodable for that pre-exchange
// hash comparison to identify them: two encoding failures both produce empty
// digests that compare equal. Production conditional storage rejects
// unencodable expected credentials before persistence. The caller decides status
// eligibility, and returned metadata is saved as supplied rather than receiving
// a new coordinator token version.
// Cancellation observed after exchange prevents starting persistence. A
// conditional-write conflict rereads the row and returns nil when it still
// exists, discarding that exchange result. Supported credential containers are
// copied; unsupported mutable values retain CloneValues sharing and need caller
// stability.
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
	// Reread under the shared lock before exchange so imported source identity
	// can be compared against administrator updates that occurred after
	// selection.
	current, err := api.providerRepo.GetByID(ctx, value.ID)
	if err != nil {
		return err
	}
	// Digest equality can also accept two encoding failures, so JSON-encodable
	// credential observations are a caller requirement for this pre-exchange gate.
	if current == nil || current.ID != value.ID || current.Status != value.Status || RefreshCredentialIdentity(current) != RefreshCredentialIdentity(value) {
		return ErrRefreshProviderStateChanged
	}
	attempted := snapshotRefreshRecord(current)
	if err := ctx.Err(); err != nil {
		return err
	}
	credentials := exchange(ctx, current)
	// A callback may return after shutdown cancellation. Check that observation
	// before persistence so its late credentials are discarded while locks
	// unwind.
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
		// A concurrent identity update takes precedence over these exchanged values.
		// Confirm the row still exists, then let its new state stand without reexchange.
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
