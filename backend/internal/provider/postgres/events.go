package postgres

import (
	"context"
	"time"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// Provider persistence emits different scheduling events for complete
// records, group membership, activity timestamps, and bulk changes. The
// application supplies the event names and payload encoding, allowing these
// storage operations to use the same scheduler outbox and snapshot publisher
// as other modules.
//
// A supplied executor determines whether an event joins a transaction or is
// written separately. Snapshot callbacks are immediate best-effort operations
// whose timing belongs to each mutation path. Grok credential containment
// uses a bounded context detached from request cancellation so a completed
// database write can still reach the local snapshot after its request ends.

// The event kinds distinguish complete record, membership, activity, and bulk
// changes for the application-supplied scheduler event encoder.
const (
	ProviderChanged       ProviderEvent = iota // complete provider record changed
	ProviderGroupsChanged                      // provider membership set changed
	ProviderLastUsed                           // observed provider activity changed
	ProviderBulkChanged                        // multiple provider records changed
)

// ProviderEvent identifies a storage change; application assembly supplies
// its outbox name and encoding.
type ProviderEvent uint8

// ProviderEvents connects store mutations to scheduler event encoding and snapshots.
// Mutation paths choose executors and callback timing; the adapter handles cache
// diagnostics because synchronization and removal methods have no error result.
type ProviderEvents interface {
	// Name returns the scheduler outbox name for a known provider event. Direct
	// SQL mutation paths require this adapter to be configured before
	// constructing their update-and-insert statements.
	Name(ProviderEvent) string
	// Write persists one event through the supplied executor and returns storage
	// failure. The mutation owns transaction completion and decides whether
	// failure rolls back its write or is observed afterward.
	Write(context.Context, postgresinfra.Executor, ProviderEvent, *int64, *int64, any) error
	// GroupPayload encodes group IDs for a provider-change or membership event.
	// The application adapter defines the payload representation, and callers
	// use it with the event whose affected groups they observed.
	GroupPayload([]int64) any
	// SyncOne reloads a provider and publishes its current scheduler snapshot
	// best-effort. The mutation caller chooses the commit and cancellation
	// timing; read and cache failures are handled inside the adapter.
	SyncOne(context.Context, int64)
	// SyncMany reloads and publishes the selected provider snapshots
	// best-effort. The production adapter deduplicates positive IDs, while
	// mutation callers decide when shared connection changes are ready to be
	// observed.
	SyncMany(context.Context, []int64)
	// Drop removes a provider snapshot best-effort after its deletion operation.
	// The caller decides when that operation is committed; cache failures are
	// handled by the adapter because this method has no error result.
	Drop(context.Context, int64)
}

// enqueue writes the supplied provider event through the configured adapter
// and executor. An absent adapter makes this operation succeed without a
// write; callers choose the transaction and decide whether adapter errors
// abort their mutation or are observed afterward.
func (r *ProviderStore) enqueue(ctx context.Context, exec postgresinfra.Executor, event ProviderEvent, providerID, groupID *int64, payload any) error {
	if r.options.Events == nil {
		return nil
	}
	return r.options.Events.Write(ctx, exec, event, providerID, groupID, payload)
}

// groupPayload delegates affected group-ID encoding to the scheduler event
// adapter. It returns nil when events are unconfigured; callers pass the
// resulting payload to their event write and retain responsibility for any
// IDs shared by their adapter.
func (r *ProviderStore) groupPayload(ids []int64) any {
	if r.options.Events == nil {
		return nil
	}
	return r.options.Events.GroupPayload(ids)
}

// eventName obtains the application's scheduler outbox name for a provider
// event. Events must be configured before direct SQL paths call it; those
// paths use the returned name in their update-and-insert statement.
func (r *ProviderStore) eventName(event ProviderEvent) string { return r.options.Events.Name(event) }

// afterChanges asks the configured adapter to synchronize the latest records
// for the supplied IDs. An absent adapter is ignored; the callback has no
// error result, and the mutation caller determines its commit and
// cancellation timing.
func (r *ProviderStore) afterChanges(ctx context.Context, ids []int64) {
	if r.options.Events != nil {
		r.options.Events.SyncMany(ctx, ids)
	}
}

// dropSnapshot asks the event adapter to remove one provider snapshot after
// deletion. It is a no-op when the adapter is absent; cache errors are
// handled by the adapter because this callback has no error result.
func (r *ProviderStore) dropSnapshot(ctx context.Context, id int64) {
	if r.options.Events != nil {
		r.options.Events.Drop(ctx, id)
	}
}

// afterChangeDetached synchronizes the latest provider snapshot using a
// two-second context that retains values from ctx while detaching its
// cancellation. Nil ctx uses background; the caller's database mutation has
// already matched before this best-effort callback begins.
func (r *ProviderStore) afterChangeDetached(ctx context.Context, providerID int64) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	// Bound detached snapshot work so a cancelled request can propagate its matched
	// credential change without leaving unbounded work on the shared storage client.
	propagationCtx, cancel := context.WithTimeout(base, 2*time.Second)
	defer cancel()
	r.afterChange(propagationCtx, providerID)
}
