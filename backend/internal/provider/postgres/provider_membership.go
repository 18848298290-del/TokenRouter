package postgres

import (
	"context"
	"errors"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbgroup "github.com/TokenFlux/TokenRouter/ent/group"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	dbprovidergroup "github.com/TokenFlux/TokenRouter/ent/providergroup"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// provider_membership.go deletes providers and replaces their group
// memberships through transaction-aware storage operations. Provider deletion
// removes group memberships and scheduled plans while Ent's normal delete hook
// retains the provider with a deletion timestamp. Membership replacement
// deletes old links and inserts the supplied set through the same transaction
// client, keeping the database's relationship changes together.
//
// Locally created transactions are committed before the snapshot removal or
// group-change notification. A store already bound to a transaction leaves
// commit and rollback to its caller, which also needs to account for immediate
// callbacks. Empty BindGroups removes all links and returns before event
// publication; nonempty replacement includes both former and selected groups
// in its notification.

// Delete removes a provider's memberships and scheduled plans before running
// Ent's provider delete operation. Normal contexts use SoftDeleteMixin to
// retain the provider row; the group IDs used for notification are read
// before opening the local transaction.
//
// A locally opened transaction commits those changes before removing the
// snapshot and attempting its event. ErrTxStarted reuses the store's
// transaction-bound client, so the outer caller owns commit while immediate
// callbacks still run here. Notification errors are observed after the
// deletion operations; an absent provider can still complete those cleanup
// steps.
func (r *ProviderStore) Delete(ctx context.Context, id int64) error {
	groupIDs, err := r.LoadProviderGroupIDs(ctx, id)
	if err != nil {
		return err
	}
	// Remove links and scheduled plans before the Ent delete hook records deletion;
	// a local rollback leaves the prior links, plans, and provider state in place.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		// Roll back locally owned deletion when removing plans or recording the
		// provider deletion fails, keeping the earlier membership cleanup from
		// committing alone.
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// The outer owner supplies this transactional client and decides whether
		// link removal, scheduled-plan deletion, and the retained provider
		// delete commit.
		txClient = r.client
	}

	if _, err := txClient.ProviderGroup.Delete().Where(dbprovidergroup.ProviderIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err := txClient.ExecContext(ctx, "DELETE FROM scheduled_test_plans WHERE provider_id = $1", id); err != nil {
		return err
	}
	if _, err := txClient.Provider.Delete().Where(dbprovider.IDEQ(id)).Exec(ctx); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Snapshot removal follows a locally owned commit; an outer transactional
	// store still reaches this callback before its caller decides
	// transaction completion.
	r.dropSnapshot(ctx, id)
	// Notify former groups after deletion so their buckets can remove the provider;
	// event errors are observed after the deletion operations have already succeeded.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, r.groupPayload(groupIDs)); err != nil {
		r.observe("[SchedulerOutbox] enqueue provider delete failed: provider=%d err=%v", id, err)
	}
	return nil
}

// AddToGroup saves one provider/group membership and then attempts its
// affected-group event. Existing-pair or other save errors are returned
// directly; event failures are observed after the association save, and this
// method performs no immediate snapshot callback. The supplied store handles
// determine transaction completion and durability.
func (r *ProviderStore) AddToGroup(ctx context.Context, providerID, groupID int64) error {
	_, err := r.client.ProviderGroup.Create().
		SetProviderID(providerID).
		SetGroupID(groupID).
		Save(ctx)
	if err != nil {
		return err
	}
	payload := r.groupPayload([]int64{groupID})
	// Notify the affected group after its association save so the scheduler can
	// rebuild that bucket; event errors are observed while the handles
	// retain transaction ownership.
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue add to group failed: provider=%d group=%d err=%v", providerID, groupID, err)
	}
	return nil
}

// RemoveFromGroup deletes the matching membership pair and then attempts an
// affected-group event. A missing pair succeeds, event failures are logged
// after the delete, and the supplied store connections determine transaction
// ownership.
func (r *ProviderStore) RemoveFromGroup(ctx context.Context, providerID, groupID int64) error {
	_, err := r.client.ProviderGroup.Delete().
		Where(
			dbprovidergroup.ProviderIDEQ(providerID),
			dbprovidergroup.GroupIDEQ(groupID),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	payload := r.groupPayload([]int64{groupID})
	// Removing a pair can also match nothing; still notify its group so the
	// scheduler can reconcile a cached membership, observing event errors separately.
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue remove from group failed: provider=%d group=%d err=%v", providerID, groupID, err)
	}
	return nil
}

// GetGroups returns converted groups currently linked to the requested
// provider ID.
func (r *ProviderStore) GetGroups(ctx context.Context, providerID int64) ([]accessview.GroupConfig, error) {
	groups, err := r.client.Group.Query().
		Where(
			dbgroup.HasProvidersWith(dbprovider.IDEQ(providerID)),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	outGroups := make([]accessview.GroupConfig, 0, len(groups))
	for i := range groups {
		outGroups = append(outGroups, *r.options.Group(groups[i]))
	}
	return outGroups, nil
}

// BindGroups replaces all memberships for providerID through one Ent
// transaction client. It records the former group IDs before replacement, and
// duplicate or invalid requested associations are left to the database's
// constraints.
//
// A locally opened transaction is committed here, while ErrTxStarted leaves
// completion with the outer caller. Empty input clears memberships and
// returns before any event write. A nonempty replacement attempts a change
// event for the union of former and selected groups after a local commit;
// event failure is observed after association changes and does not fail the
// method.
func (r *ProviderStore) BindGroups(ctx context.Context, providerID int64, groupIDs []int64) error {
	existingGroupIDs, err := r.LoadProviderGroupIDs(ctx, providerID)
	if err != nil {
		return err
	}
	// Delete then insert through one transaction client so a failed new association
	// rolls back the old links when this method owns that transaction.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		// Keep the old membership set recoverable when a replacement insert fails;
		// this rollback belongs to the locally opened transaction that held
		// the deletion.
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// An outer transaction already owns this client. Keep membership replacement
		// on its connection and leave commit or rollback to that owner.
		txClient = r.client
	}

	if _, err := txClient.ProviderGroup.Delete().Where(dbprovidergroup.ProviderIDEQ(providerID)).Exec(ctx); err != nil {
		return err
	}

	// Clearing the selection finishes here after old links are removed; this early
	// path has no payload publication and leaves commit to an outer owner when present.
	if len(groupIDs) == 0 {
		if tx != nil {
			return tx.Commit()
		}
		return nil
	}

	builders := make([]*dbent.ProviderGroupCreate, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		builders = append(builders, txClient.ProviderGroup.Create().
			SetProviderID(providerID).
			SetGroupID(groupID),
		)
	}

	if _, err := txClient.ProviderGroup.CreateBulk(builders...).Save(ctx); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Include both former and selected groups so the event consumer can rebuild
	// buckets that lost this provider as well as buckets that gained it.
	payload := r.groupPayload(MergeGroupIDs(existingGroupIDs, groupIDs))
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue bind groups failed: provider=%d err=%v", providerID, err)
	}
	return nil
}

// LoadProviderGroupIDs returns stored membership group IDs for one provider
// ID.
func (r *ProviderStore) LoadProviderGroupIDs(ctx context.Context, providerID int64) ([]int64, error) {
	entries, err := r.client.ProviderGroup.
		Query().
		Where(dbprovidergroup.ProviderIDEQ(providerID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.GroupID)
	}
	return ids, nil
}

// MergeGroupIDs returns positive unique IDs from a followed by b. First
// occurrences determine output order, zero and negative values are omitted,
// and the new slice can be changed independently of either input slice.
func MergeGroupIDs(a []int64, b []int64) []int64 {
	seen := make(map[int64]struct{}, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, id := range a {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range b {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
