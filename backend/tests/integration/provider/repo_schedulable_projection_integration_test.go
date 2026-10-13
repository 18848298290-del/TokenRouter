//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// The schedulable load query needs the same candidate IDs and order as the full
// provider query. PostgreSQL fixtures compare both results across disabled,
// expired, overloaded, rate-limited, and temporarily blocked states, and check
// load-factor and concurrency defaults.

// TestListSchedulableProviderLoadsMatchesListSchedulable creates PostgreSQL
// providers spanning configuration, health-window, and expiry conditions.
// Lightweight and full queries return the same IDs and order, with matching
// effective load values and the expected default of one.
func TestListSchedulableProviderLoadsMatchesListSchedulable(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newProviderStoreContract(client, tx, nil)
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	create := func(name string) *providercore.Record {
		return mustCreateProvider(t, client, &providercore.Record{Name: name, Schedulable: true})
	}

	positiveLoad := create("projection-positive-load")
	_, err := client.Provider.UpdateOneID(positiveLoad.ID).SetConcurrency(2).SetLoadFactor(9).SetPriority(30).Save(ctx)
	require.NoError(t, err)
	concurrencyFallback := create("projection-concurrency-fallback")
	_, err = client.Provider.UpdateOneID(concurrencyFallback.ID).SetConcurrency(4).SetPriority(10).Save(ctx)
	require.NoError(t, err)
	zeroFallback := create("projection-zero-fallback")
	_, err = client.Provider.UpdateOneID(zeroFallback.ID).SetConcurrency(0).SetLoadFactor(0).SetPriority(20).Save(ctx)
	require.NoError(t, err)

	disabled := create("projection-disabled")
	_, err = client.Provider.UpdateOneID(disabled.ID).SetStatus(billing.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	unschedulable := create("projection-unschedulable")
	_, err = client.Provider.UpdateOneID(unschedulable.ID).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	expired := create("projection-expired")
	_, err = client.Provider.UpdateOneID(expired.ID).SetExpiresAt(past).SetAutoPauseOnExpired(true).Save(ctx)
	require.NoError(t, err)
	expiredAllowed := create("projection-expired-allowed")
	_, err = client.Provider.UpdateOneID(expiredAllowed.ID).SetExpiresAt(past).SetAutoPauseOnExpired(false).Save(ctx)
	require.NoError(t, err)
	overloaded := create("projection-overloaded")
	_, err = client.Provider.UpdateOneID(overloaded.ID).SetOverloadUntil(future).Save(ctx)
	require.NoError(t, err)
	overloadCleared := create("projection-overload-cleared")
	_, err = client.Provider.UpdateOneID(overloadCleared.ID).SetOverloadUntil(past).Save(ctx)
	require.NoError(t, err)
	rateLimited := create("projection-rate-limited")
	_, err = client.Provider.UpdateOneID(rateLimited.ID).SetRateLimitResetAt(future).Save(ctx)
	require.NoError(t, err)
	rateLimitCleared := create("projection-rate-limit-cleared")
	_, err = client.Provider.UpdateOneID(rateLimitCleared.ID).SetRateLimitResetAt(past).Save(ctx)
	require.NoError(t, err)
	tempBlocked := create("projection-temp-blocked")
	_, err = client.Provider.UpdateOneID(tempBlocked.ID).SetTempUnschedulableUntil(future).Save(ctx)
	require.NoError(t, err)
	tempCleared := create("projection-temp-cleared")
	_, err = client.Provider.UpdateOneID(tempCleared.ID).SetTempUnschedulableUntil(past).Save(ctx)
	require.NoError(t, err)

	providers, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	loads, err := repo.ListSchedulableProviderLoads(ctx)
	require.NoError(t, err)

	providerIDs := make([]int64, 0, len(providers))
	wantByID := make(map[int64]int, len(providers))
	for i := range providers {
		providerIDs = append(providerIDs, providers[i].ID)
		wantByID[providers[i].ID] = providers[i].EffectiveLoadFactor()
	}

	loadIDs := make([]int64, 0, len(loads))
	byID := make(map[int64]int, len(loads))
	for _, load := range loads {
		loadIDs = append(loadIDs, load.ID)
		byID[load.ID] = load.MaxConcurrency
	}
	require.Equal(t, providerIDs, loadIDs)
	targetIDs := map[int64]struct{}{
		positiveLoad.ID: {}, concurrencyFallback.ID: {}, zeroFallback.ID: {},
	}
	targetOrder := make([]int64, 0, len(targetIDs))
	for _, id := range loadIDs {
		if _, ok := targetIDs[id]; ok {
			targetOrder = append(targetOrder, id)
		}
	}
	require.Equal(t, []int64{concurrencyFallback.ID, zeroFallback.ID, positiveLoad.ID}, targetOrder)
	require.Equal(t, wantByID, byID)
	require.Equal(t, 9, byID[positiveLoad.ID])
	require.Equal(t, 4, byID[concurrencyFallback.ID])
	require.Equal(t, 1, byID[zeroFallback.ID])
	for _, included := range []*providercore.Record{expiredAllowed, overloadCleared, rateLimitCleared, tempCleared} {
		require.Contains(t, byID, included.ID)
	}
	for _, excluded := range []*providercore.Record{disabled, unschedulable, expired, overloaded, rateLimited, tempBlocked} {
		require.NotContains(t, byID, excluded.ID)
	}
}
