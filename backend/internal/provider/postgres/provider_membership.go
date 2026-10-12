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

func (r *ProviderStore) Delete(ctx context.Context, id int64) error {
	groupIDs, err := r.LoadProviderGroupIDs(ctx, id)
	if err != nil {
		return err
	}
	// Delete the provider and its group memberships in one transaction.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// ErrTxStarted means the client already belongs to the caller's transaction.
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
	r.dropSnapshot(ctx, id)
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, r.groupPayload(groupIDs)); err != nil {
		r.observe("[SchedulerOutbox] enqueue provider delete failed: provider=%d err=%v", id, err)
	}
	return nil
}

func (r *ProviderStore) AddToGroup(ctx context.Context, providerID, groupID int64) error {
	_, err := r.client.ProviderGroup.Create().
		SetProviderID(providerID).
		SetGroupID(groupID).
		Save(ctx)
	if err != nil {
		return err
	}
	payload := r.groupPayload([]int64{groupID})
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue add to group failed: provider=%d group=%d err=%v", providerID, groupID, err)
	}
	return nil
}

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
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue remove from group failed: provider=%d group=%d err=%v", providerID, groupID, err)
	}
	return nil
}

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

func (r *ProviderStore) BindGroups(ctx context.Context, providerID int64, groupIDs []int64) error {
	existingGroupIDs, err := r.LoadProviderGroupIDs(ctx, providerID)
	if err != nil {
		return err
	}
	// Replace old and new group memberships atomically.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// ErrTxStarted means the client already belongs to the caller's transaction.
		txClient = r.client
	}

	if _, err := txClient.ProviderGroup.Delete().Where(dbprovidergroup.ProviderIDEQ(providerID)).Exec(ctx); err != nil {
		return err
	}

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
	payload := r.groupPayload(MergeGroupIDs(existingGroupIDs, groupIDs))
	if err := r.enqueue(ctx, r.sql, ProviderGroupsChanged, &providerID, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue bind groups failed: provider=%d err=%v", providerID, err)
	}
	return nil
}

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
