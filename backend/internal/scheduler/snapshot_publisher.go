package scheduler

import "context"

// snapshot_publisher.go publishes committed provider records to the cache and
// removes deleted providers.
// Committed provider changes can update their cached records immediately
// before background bucket rebuilds catch up. SnapshotPublisher reads the
// committed records and attempts cache publication, logging failures so the
// durable outbox and periodic rebuild paths can later refresh scheduling
// state.

// SnapshotPublicationCache accepts committed provider records for immediate
// scheduler cache publication.
type SnapshotPublicationCache interface {
	// SetProvider publishes the supplied committed provider record for scheduler
	// reads. Database writers invoke it after commit and retain their
	// transactional outbox event so a publication error remains recoverable
	// through later rebuilds.
	SetProvider(context.Context, SnapshotProvider) error
	// DeleteProvider removes the scheduler's cached record for a deleted provider
	// ID. Database writers call it after deletion commits, with their outbox
	// event supplying subsequent affected-bucket reconstruction.
	DeleteProvider(context.Context, int64) error
}

// SnapshotPublisher reads committed provider records and logs cache
// publication failures for database writers.
type SnapshotPublisher struct {
	// Read supplies committed provider metadata for a positive-ID Publish call
	// when Cache is present. Callers bind a nonnil callback and keep its source
	// available throughout publication.
	Read func(context.Context, int64) (SnapshotProvider, error)
	// ReadMany supplies committed records for the distinct positive IDs chosen by
	// PublishMany. A nonnil callback is required when Cache is present and that
	// filtered ID set is nonempty.
	ReadMany func(context.Context, []int64) ([]SnapshotProvider, error)
	// Cache may be nil to skip immediate publication. Persistent writers still
	// rely on their transactional outbox events to update bucket memberships.
	Cache       SnapshotPublicationCache // immediate provider publication cache
	Diagnostics Diagnostics              // publication failure logging callbacks
}

// Publish reads one committed provider and attempts to update its cached
// scheduler record. Nonpositive IDs or an absent cache skip the operation;
// read and publication failures are logged and left for durable recovery.
func (p SnapshotPublisher) Publish(ctx context.Context, providerID int64) {
	if p.Cache == nil || providerID <= 0 {
		return
	}
	provider, err := p.Read(ctx, providerID)
	// Database commit has already succeeded at publication callers. Log this read
	// failure and leave the durable outbox available to retry scheduling
	// visibility later.
	if err != nil {
		p.Diagnostics.printf("repository.provider", "[Scheduler] sync provider snapshot read failed: id=%d err=%v", providerID, err)
		return
	}
	// Immediate publication can fail after the record commits. Report the delayed
	// visibility here while the persisted change remains available to later
	// rebuilds.
	if err := p.Cache.SetProvider(ctx, provider); err != nil {
		p.Diagnostics.printf("repository.provider", "[Scheduler] sync provider snapshot write failed: id=%d err=%v", providerID, err)
	}
}

// PublishMany reads distinct positive provider IDs and attempts publication
// for each returned record. It skips nil records, logs failed reads or writes,
// and continues with later records after a write failure.
func (p SnapshotPublisher) PublishMany(ctx context.Context, providerIDs []int64) {
	if p.Cache == nil || len(providerIDs) == 0 {
		return
	}

	uniqueIDs := make([]int64, 0, len(providerIDs))
	seen := make(map[int64]struct{}, len(providerIDs))
	for _, id := range providerIDs {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return
	}

	// Read the committed batch once after removing duplicate IDs. A failed read
	// leaves all immediate publication to later outbox or periodic rebuild work.
	providers, err := p.ReadMany(ctx, uniqueIDs)
	if err != nil {
		p.Diagnostics.printf("repository.provider", "[Scheduler] batch sync provider snapshot read failed: count=%d err=%v", len(uniqueIDs), err)
		return
	}

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		// Each record can publish independently. Log a failed update and continue
		// so one cache failure does not prevent other committed providers from becoming visible.
		if err := p.Cache.SetProvider(ctx, provider); err != nil {
			p.Diagnostics.printf("repository.provider", "[Scheduler] batch sync provider snapshot write failed: id=%d err=%v", provider.SnapshotMetadata().ID, err)
		}
	}
}

// Drop removes a deleted provider's cached scheduler record after its database
// deletion commits. Nonpositive IDs or an absent cache skip deletion, and
// cache errors are logged for later outbox-driven recovery.
func (p SnapshotPublisher) Drop(ctx context.Context, providerID int64) {
	if p.Cache == nil || providerID <= 0 {
		return
	}
	// The persistent deletion remains committed if cache removal fails. Its outbox
	// event rebuilds affected membership while this diagnostic records the delayed visibility.
	if err := p.Cache.DeleteProvider(ctx, providerID); err != nil {
		p.Diagnostics.printf("repository.provider", "[Scheduler] delete provider snapshot failed: id=%d err=%v", providerID, err)
	}
}
