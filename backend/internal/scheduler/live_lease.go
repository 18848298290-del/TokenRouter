package scheduler

import "context"

// live_lease.go defines the cache operations used to acquire, renew, and
// release Live connection capacity.
// Live sessions occupy lease memberships for a provider, user, and API key
// while their connection remains active. The cache supports acquisition,
// renewal, and removal of those memberships, including transfer from
// already-owned ordinary request slots during connection setup.

// LiveConcurrencyCache maintains long-session lease memberships alongside
// ordinary provider and user request slots.
type LiveConcurrencyCache interface {
	// AcquireLiveLease reserves long-session memberships under leaseID and
	// reports capacity admission. Callers supply positive provider, user, and API
	// key IDs and a nonempty lease ID. Provider and user limits at or below zero
	// disable their respective capacity checks. The caller renews a granted lease
	// and removes it when the session ends.
	//
	// Replacing ordinary slots permits a temporary additional membership while
	// the caller transfers resources; the caller subsequently releases its
	// ordinary slots. The Redis implementation accepts an existing provider-side
	// lease ID immediately after expiry pruning, so repeated acquisition does not
	// renew every membership. Callers use RefreshLiveLease for renewal and treat
	// cache errors as an unconfirmed acquisition.
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
	// RefreshLiveLease renews the provider, user, and API key memberships using
	// the acquisition identifiers. Callers treat renewal errors as unconfirmed
	// cache operations. With valid identifiers and no cache error, the Redis
	// adapter reports false after finding a missing membership; a nil cache or
	// empty lease ID also returns false. Session owners cancel or terminate after
	// confirmed loss.
	RefreshLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) (bool, error)
	// ReleaseLiveLease removes this session's provider, user, and API key
	// memberships. Callers use the same identifiers as acquisition; removal
	// errors leave cleanup unconfirmed and the Redis adapter's remaining
	// memberships expire through their lease lifetime.
	ReleaseLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) error
}
