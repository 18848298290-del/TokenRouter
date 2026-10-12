package scheduler

import "context"

// LiveConcurrencyCache manages long-lived leases counted against provider, user, and API key limits.
type LiveConcurrencyCache interface {
	AcquireLiveLease(
		ctx context.Context,
		providerID int64,
		providerMax int,
		userID int64,
		userMax int,
		apiKeyID int64,
		leaseID string,
		replacingRegularSlots bool,
	) (bool, error)
	RefreshLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) (bool, error)
	ReleaseLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) error
}
