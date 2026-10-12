package postgres

import (
	"context"
	"time"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

const (
	ProviderChanged ProviderEvent = iota
	ProviderGroupsChanged
	ProviderLastUsed
	ProviderBulkChanged
)

// ProviderEvent identifies a storage change; application assembly supplies its outbox name and encoding.
type ProviderEvent uint8

// ProviderEvents encodes and writes outbox events and publishes cache updates after commit.
type ProviderEvents interface {
	Name(ProviderEvent) string
	Write(context.Context, postgresinfra.Executor, ProviderEvent, *int64, *int64, any) error
	GroupPayload([]int64) any
	SyncOne(context.Context, int64)
	SyncMany(context.Context, []int64)
	Drop(context.Context, int64)
}

func (r *ProviderStore) enqueue(ctx context.Context, exec postgresinfra.Executor, event ProviderEvent, providerID, groupID *int64, payload any) error {
	if r.options.Events == nil {
		return nil
	}
	return r.options.Events.Write(ctx, exec, event, providerID, groupID, payload)
}

func (r *ProviderStore) groupPayload(ids []int64) any {
	if r.options.Events == nil {
		return nil
	}
	return r.options.Events.GroupPayload(ids)
}

func (r *ProviderStore) eventName(event ProviderEvent) string { return r.options.Events.Name(event) }

func (r *ProviderStore) afterChanges(ctx context.Context, ids []int64) {
	if r.options.Events != nil {
		r.options.Events.SyncMany(ctx, ids)
	}
}

func (r *ProviderStore) dropSnapshot(ctx context.Context, id int64) {
	if r.options.Events != nil {
		r.options.Events.Drop(ctx, id)
	}
}

// afterChangeDetached propagates the latest provider snapshot with a two-second timeout independent of request cancellation.
func (r *ProviderStore) afterChangeDetached(ctx context.Context, providerID int64) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	propagationCtx, cancel := context.WithTimeout(base, 2*time.Second)
	defer cancel()
	r.afterChange(propagationCtx, providerID)
}
