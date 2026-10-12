//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestListDueOllamaCloudUsageProvidersOrderingLimitAndProxyHydration(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	proxy := mustCreateProxy(t, tx.Client(), &egress.Proxy{
		Name: "ollama-due-proxy", Protocol: "http", Host: "127.0.0.1", Port: 3128,
		Username: "user", Password: "pass", Status: providercore.StatusActive,
	})

	createProvider := func(name, baseURL string, proxyID *int64, snapshot map[string]any, lastUsed *time.Time) *providercore.Record {
		t.Helper()
		extra := map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
		}
		if snapshot != nil {
			extra[providercore.OllamaCloudUsageSnapshotExtraKey] = snapshot
		}
		return mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: name, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": name, "base_url": baseURL},
			Extra:       extra, ProxyID: proxyID, LastUsedAt: lastUsed,
		})
	}

	uppercasePath := createProvider("ollama-uppercase-path", "https://ollama.com/V1", nil, nil, nil)
	missingSnapshot := createProvider("ollama-due-missing", "HTTPS://WWW.OLLAMA.COM:443/v1", &proxy.ID, nil, nil)
	fetched := now.Add(-2 * time.Hour)
	activity := now.Add(-5 * time.Minute)
	due := createProvider("ollama-due-activity", "https://ollama.com", nil, map[string]any{
		"status":          providercore.OllamaCloudUsageStatusOK,
		"fetched_at":      fetched.UTC().Format(time.RFC3339Nano),
		"last_attempt_at": fetched.UTC().Format(time.RFC3339Nano),
		"next_refresh_at": fetched.Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}, &activity)
	// A successful snapshot with no later activity is not due for refresh.
	_ = createProvider("ollama-not-due-idle", "https://ollama.com", nil, map[string]any{
		"status":          providercore.OllamaCloudUsageStatusOK,
		"fetched_at":      now.Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		"last_attempt_at": now.Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		"next_refresh_at": now.Add(-time.Minute).UTC().Format(time.RFC3339Nano),
	}, nil)
	_ = createProvider("ollama-ineligible", "https://ollama.com.evil.test", nil, nil, nil)

	providers, err := repo.ListDueOllamaCloudUsageProviders(ctx, now, time.Minute, time.Hour, 2)

	require.NoError(t, err)
	require.Len(t, providers, 2)
	require.Equal(t, missingSnapshot.ID, providers[0].ID)
	require.Equal(t, due.ID, providers[1].ID)
	require.NotNil(t, providers[1].LastUsedAt)
	require.WithinDuration(t, activity.UTC(), providers[1].LastUsedAt.UTC(), time.Second)
	require.NotContains(t, providerIDs(providers), uppercasePath.ID)
	require.NotNil(t, providers[0].Proxy)
	require.Equal(t, proxy.ID, providers[0].Proxy.ID)
	require.Equal(t, proxy.URL(), providers[0].Proxy.URL())
}

// TestListDueOllamaCloudUsageProvidersParsesAllRFC3339Precisions exercises SQL parsing
// with the fractional precision and timezone forms written to the database.
//
// Each snapshot was fetched about two minutes ago, with activity thirty seconds ago.
// Debounce and the minimum fetch interval keep it from being due. A parse failure becomes NULL
// and takes the fail-open branch, so these records must be absent to detect parser failures.
//
//   - Go UTC timestamps end in Z. PostgreSQL 14-16 require rewriting that suffix to +00:00
//     before jsonpath .datetime(); otherwise these fixtures appear due.
//     PostgreSQL 17 and later accept Z directly.
//   - Seven to nine fractional digits exceed the parser's microsecond precision and need truncation.
//
// Run against an older supported PostgreSQL version to exercise that compatibility path:
//
//	TOKENROUTER_TEST_POSTGRES_IMAGE=postgres:15-alpine go test -tags integration ./tests/integration/provider/
func TestListDueOllamaCloudUsageProvidersParsesAllRFC3339Precisions(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 22, 14, 0, 0, 0, time.UTC)
	activity := now.Add(-30 * time.Second)

	// These values are about two minutes old, with different fractional precision and timezone notation.
	notDue := map[string]string{
		"nano-z":         "2026-07-22T13:58:00.123456789Z",
		"eight-positive": "2026-07-22T14:58:00.12345678+01:00",
		"seven-negative": "2026-07-22T11:58:00.1234567-02:00",
	}
	for name, fetchedAt := range notDue {
		_ = mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: "ollama-precision-" + name, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "precision-" + name, "base_url": "https://ollama.com"},
			Extra: map[string]any{
				providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
				providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
				providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
					"status":          providercore.OllamaCloudUsageStatusOK,
					"fetched_at":      fetchedAt,
					"last_attempt_at": fetchedAt,
				},
			},
			LastUsedAt: &activity,
		})
	}

	// Include a truly due record so an empty result cannot make the test pass.
	staleFetched := now.Add(-2 * time.Hour)
	due := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-precision-due", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "precision-due", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status":          providercore.OllamaCloudUsageStatusOK,
				"fetched_at":      staleFetched.UTC().Format(time.RFC3339Nano),
				"last_attempt_at": staleFetched.UTC().Format(time.RFC3339Nano),
			},
		},
		LastUsedAt: &activity,
	})

	providers, err := repo.ListDueOllamaCloudUsageProviders(ctx, now, time.Minute, time.Hour, 10)

	require.NoError(t, err)
	ids := providerIDs(providers)
	require.Contains(t, ids, due.ID, "a stale snapshot with fresh activity must be due")
	require.Len(t, ids, 1,
		"only the stale group may be due; extra rows mean a timestamp failed to parse and fell into the fail-open branch")
}

func TestListDueOllamaCloudUsageProvidersUsesGroupMaxLastUsedAndFailsOpen(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 22, 14, 0, 0, 0, time.UTC)
	fetched := now.Add(-30 * time.Minute)
	older := now.Add(-10 * time.Minute)
	newer := now.Add(-2 * time.Minute)

	leader := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-group-leader", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "shared-key", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status":          providercore.OllamaCloudUsageStatusOK,
				"fetched_at":      fetched.UTC().Format(time.RFC3339Nano),
				"last_attempt_at": fetched.UTC().Format(time.RFC3339Nano),
				"next_refresh_at": fetched.Add(time.Hour).UTC().Format(time.RFC3339Nano),
			},
		},
		LastUsedAt: &older,
	})
	_ = mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-group-sibling", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "shared-key", "base_url": "https://www.ollama.com/v1"},
		LastUsedAt:  &newer,
	})
	invalid := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-invalid-snapshot", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "invalid-key", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status": providercore.OllamaCloudUsageStatusOK, "fetched_at": "2026-02-30T09:00:00.123456789Z",
			},
		},
	})
	idle := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-idle-ok", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "idle-key", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status":          providercore.OllamaCloudUsageStatusOK,
				"fetched_at":      fetched.UTC().Format(time.RFC3339Nano),
				"last_attempt_at": fetched.UTC().Format(time.RFC3339Nano),
				"next_refresh_at": fetched.Add(time.Hour).UTC().Format(time.RFC3339Nano),
			},
		},
	})

	providers, err := repo.ListDueOllamaCloudUsageProviders(ctx, now, time.Minute, time.Hour, 10)

	require.NoError(t, err, "invalid stored values must not abort the query")
	ids := providerIDs(providers)
	require.Contains(t, ids, invalid.ID)
	require.Contains(t, ids, leader.ID)
	require.NotContains(t, ids, idle.ID)
	for _, provider := range providers {
		if provider.ID == leader.ID {
			require.NotNil(t, provider.LastUsedAt)
			require.WithinDuration(t, newer.UTC(), provider.LastUsedAt.UTC(), time.Second,
				"group MAX(last_used_at) must come from the sibling")
		}
	}
}

func TestLockAndMergeProviderManagedExtraCoalescesNullableOllamaGroupIdentity(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	provider := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ordinary-openai-without-base-url", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-no-base-url"},
		Extra:       map[string]any{"custom": true},
	})
	loaded, err := newProviderStoreContract(tx.Client(), tx, nil).GetByID(ctx, provider.ID)
	require.NoError(t, err)

	merged, err := newProviderStoreContract(tx.Client(), tx, nil).LockAndMergeProviderManagedExtra(ctx, tx.Client(), loaded)

	require.NoError(t, err, "a NULL Ollama eligibility expression must scan as false")
	require.NotContains(t, merged, providercore.OllamaCloudUsageSessionExtraKey)
	require.Equal(t, true, merged["custom"])
}

func TestOllamaCloudUsageGroupWritesAreAtomicAcrossPlatformsAndURLVariants(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	create := func(name, platform, apiKey, baseURL string) *providercore.Record {
		t.Helper()
		return mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: name, Platform: platform, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": apiKey, "base_url": baseURL},
			Extra:       map[string]any{},
		})
	}
	first := create("ollama-group-openai", capability.PlatformOpenAI, "shared-key", "https://ollama.com")
	second := create("ollama-group-anthropic", capability.PlatformAnthropic, "shared-key", "HTTPS://WWW.OLLAMA.COM:443/v1")
	different := create("ollama-group-different", capability.PlatformOpenAI, "different-key", "https://ollama.com")

	require.NoError(t, repo.SaveOllamaCloudUsageSession(ctx, first, "cipher:shared", false))
	for _, id := range []int64{first.ID, second.ID} {
		provider, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "cipher:shared", provider.Extra[providercore.OllamaCloudUsageSessionExtraKey])
		require.Equal(t, false, provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	}
	differentLoaded, err := repo.GetByID(ctx, different.ID)
	require.NoError(t, err)
	require.NotContains(t, differentLoaded.Extra, providercore.OllamaCloudUsageSessionExtraKey)

	secondLoaded, err := repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.NoError(t, repo.SetOllamaCloudUsageAutoRefresh(ctx, secondLoaded, true))
	firstLoaded, err := repo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	secondLoaded, err = repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, true, firstLoaded.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.Equal(t, true, secondLoaded.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])

	now := time.Now().UTC()
	snapshot := &providercore.OllamaCloudUsageSnapshot{
		Status: providercore.OllamaCloudUsageStatusOK, LastAttemptAt: now, NextRefreshAt: now.Add(time.Hour),
	}
	require.NoError(t, repo.UpdateOllamaCloudUsageSnapshot(ctx, firstLoaded, snapshot))
	secondLoaded, err = repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	storedSnapshot, ok := secondLoaded.Extra[providercore.OllamaCloudUsageSnapshotExtraKey].(map[string]any)
	require.True(t, ok, "stored Ollama snapshot shape")
	require.Equal(t, providercore.OllamaCloudUsageStatusOK, storedSnapshot["status"])

	staleSecond := secondLoaded
	require.NoError(t, repo.UpdateCredentials(ctx, second.ID, map[string]any{
		"api_key": "rotated-key", "base_url": "https://ollama.com",
	}))
	require.ErrorIs(t, repo.DisableOllamaCloudUsageAutoRefresh(ctx, staleSecond), providercore.ErrOllamaCloudUsageIdentityChanged)
	firstLoaded, err = repo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	secondLoaded, err = repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, "cipher:shared", firstLoaded.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, true, firstLoaded.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.NotContains(t, secondLoaded.Extra, providercore.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, secondLoaded.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)

	require.NoError(t, repo.DeleteOllamaCloudUsageSession(ctx, firstLoaded))
	firstLoaded, err = repo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.NotContains(t, firstLoaded.Extra, providercore.OllamaCloudUsageSessionExtraKey)
}

func TestConcurrentOllamaCloudUsageSaveAndDeleteSerializeGroupState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newProviderStoreContract(client, integrationDB, nil)
	suffix := time.Now().UnixNano()
	apiKey := fmt.Sprintf("ollama-concurrent-%d", suffix)
	create := func(platform string) *providercore.Record {
		t.Helper()
		return mustCreateProvider(t, client, &providercore.Record{
			Name: fmt.Sprintf("%s-%s", apiKey, platform), Platform: platform, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": apiKey, "base_url": "https://ollama.com"},
			Extra: map[string]any{
				providercore.OllamaCloudUsageSessionExtraKey:     "cipher:initial",
				providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			},
		})
	}
	first := create(capability.PlatformOpenAI)
	second := create(capability.PlatformAnthropic)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM providers WHERE id IN ($1, $2)", first.ID, second.ID)
	})
	anchor, err := repo.GetByID(ctx, first.ID)
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs <- repo.SaveOllamaCloudUsageSession(ctx, anchor, "cipher:replacement", true)
	}()
	go func() {
		defer wg.Done()
		<-start
		errs <- repo.DeleteOllamaCloudUsageSession(ctx, anchor)
	}()
	close(start)
	wg.Wait()
	close(errs)
	for writeErr := range errs {
		require.NoError(t, writeErr)
	}

	firstLoaded, err := repo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	secondLoaded, err := repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	managedState := func(provider *providercore.Record) map[string]any {
		state := make(map[string]any)
		for _, key := range []string{
			providercore.OllamaCloudUsageSessionExtraKey,
			providercore.OllamaCloudUsageAutoRefreshExtraKey,
			providercore.OllamaCloudUsageSnapshotExtraKey,
		} {
			if value, ok := provider.Extra[key]; ok {
				state[key] = value
			}
		}
		return state
	}
	firstState := managedState(firstLoaded)
	require.Equal(t, firstState, managedState(secondLoaded), "a serialized last commit must own the whole group")
	if len(firstState) > 0 {
		require.Equal(t, "cipher:replacement", firstState[providercore.OllamaCloudUsageSessionExtraKey])
		require.Equal(t, true, firstState[providercore.OllamaCloudUsageAutoRefreshExtraKey])
		require.NotContains(t, firstState, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
}

func providerIDs(providers []providercore.Record) []int64 {
	ids := make([]int64, len(providers))
	for index := range providers {
		ids[index] = providers[index].ID
	}
	return ids
}

func TestOllamaCloudUsageCredentialAndBulkUpdatesPreserveManagedStateOnlyWhenSafe(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	now := time.Now().UTC()
	newProvider := func(name string) *providercore.Record {
		return mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: name, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "old-key", "base_url": "https://ollama.com"},
			Extra: map[string]any{
				providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
				providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
				providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
					"status": providercore.OllamaCloudUsageStatusOK, "last_attempt_at": now, "next_refresh_at": now.Add(time.Hour),
				},
			},
		})
	}

	rawProvider := newProvider("ollama-raw-credentials")
	require.NoError(t, repo.UpdateCredentials(ctx, rawProvider.ID, map[string]any{
		"api_key": "old-key", "base_url": "https://ollama.com/V1",
	}))
	rawUpdated, err := repo.GetByID(ctx, rawProvider.ID)
	require.NoError(t, err)
	require.NotContains(t, rawUpdated.Extra, providercore.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, rawUpdated.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, rawUpdated.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)

	bulkProvider := newProvider("ollama-bulk-credentials")
	rows, err := repo.BulkUpdate(ctx, []int64{bulkProvider.ID}, providercore.ProviderBulkUpdate{
		Credentials: map[string]any{"base_url": "HTTPS://WWW.OLLAMA.COM:443/v1"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	bulkUnchanged, err := repo.GetByID(ctx, bulkProvider.ID)
	require.NoError(t, err)
	require.Contains(t, bulkUnchanged.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)

	rows, err = repo.BulkUpdate(ctx, []int64{bulkProvider.ID}, providercore.ProviderBulkUpdate{
		Credentials: map[string]any{"base_url": "https://ollama.com/V1"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	bulkIneligible, err := repo.GetByID(ctx, bulkProvider.ID)
	require.NoError(t, err)
	require.NotContains(t, bulkIneligible.Extra, providercore.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, bulkIneligible.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, bulkIneligible.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
}

func TestProxyIdentityUpdateInvalidatesOllamaSnapshotAndRejectsInFlightCAS(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	providerRepo := newProviderStoreContract(tx.Client(), tx, nil)
	proxyRepo := newProxyStoreContract(tx.Client(), tx)
	proxy := mustCreateProxy(t, tx.Client(), &egress.Proxy{
		Name: "ollama-identity-proxy", Protocol: "http", Host: "old.example", Port: 8080,
		Username: "old-user", Password: "old-pass", Status: providercore.StatusActive,
	})
	now := time.Now().UTC()
	provider := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-proxy-provider", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://ollama.com"},
		ProxyID:     &proxy.ID,
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status": providercore.OllamaCloudUsageStatusOK, "last_attempt_at": now, "next_refresh_at": now.Add(time.Hour),
			},
		},
	})
	inFlight, err := providerRepo.GetByID(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, inFlight.Proxy)
	require.Equal(t, "old.example", inFlight.Proxy.Host)

	proxyToUpdate, err := proxyRepo.GetByID(ctx, proxy.ID)
	require.NoError(t, err)
	proxyToUpdate.Host = "new.example"
	require.NoError(t, proxyRepo.Update(ctx, proxyToUpdate))

	got, err := providerRepo.GetByID(ctx, provider.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	require.Equal(t, "cipher:wos-session=fixture", got.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, true, got.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])

	err = providerRepo.UpdateOllamaCloudUsageSnapshot(ctx, inFlight, &providercore.OllamaCloudUsageSnapshot{
		Status: providercore.OllamaCloudUsageStatusOK, LastAttemptAt: now, NextRefreshAt: now.Add(time.Hour),
	})
	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageIdentityChanged)
}

// TestUpdateCredentialsPreservesOllamaAndDiscardsDeprecatedExtra checks retired-field cleanup, unchanged Ollama
// identity, and token-rotation invalidation.
func TestUpdateCredentialsPreservesOllamaAndDiscardsDeprecatedExtra(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)

	legacyProvider := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "openai-legacy-extra", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-legacy", "base_url": "https://relay.example.com/v1"},
		Extra: map[string]any{
			deprecatedUpstreamBillingProbeEnabledExtraKey: true,
			deprecatedUpstreamBillingProbeExtraKey:        map[string]any{"status": "ok"},
			"custom":                                      "value",
		},
	})
	require.NoError(t, repo.UpdateCredentials(ctx, legacyProvider.ID, map[string]any{
		"api_key": "sk-legacy", "base_url": "https://relay.example.com/v1",
	}))
	legacyLoaded, err := repo.GetByID(ctx, legacyProvider.ID)
	require.NoError(t, err)
	require.NotContains(t, legacyLoaded.Extra, deprecatedUpstreamBillingProbeExtraKey)
	require.NotContains(t, legacyLoaded.Extra, deprecatedUpstreamBillingProbeEnabledExtraKey)
	require.Equal(t, "value", legacyLoaded.Extra["custom"])

	now := time.Now().UTC()
	ollamaProvider := mustCreateProvider(t, tx.Client(), &providercore.Record{
		Name: "ollama-unchanged", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "ollama-key", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
				"status": providercore.OllamaCloudUsageStatusOK, "last_attempt_at": now, "next_refresh_at": now.Add(time.Hour),
			},
		},
	})
	require.NoError(t, repo.UpdateCredentials(ctx, ollamaProvider.ID, map[string]any{
		"api_key": "ollama-key", "base_url": "https://ollama.com",
	}))
	ollamaLoaded, err := repo.GetByID(ctx, ollamaProvider.ID)
	require.NoError(t, err)
	require.Equal(t, "cipher:wos-session=fixture", ollamaLoaded.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, true, ollamaLoaded.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.Contains(t, ollamaLoaded.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)

	require.NoError(t, repo.UpdateCredentials(ctx, ollamaProvider.ID, map[string]any{
		"api_key": "ollama-key-rotated", "base_url": "https://ollama.com",
	}))
	ollamaLoaded, err = repo.GetByID(ctx, ollamaProvider.ID)
	require.NoError(t, err)
	require.NotContains(t, ollamaLoaded.Extra, providercore.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, ollamaLoaded.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, ollamaLoaded.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
}

// TestListDueOllamaCloudUsageProvidersSQLDueRulesMatchService checks that SQL applies debounce,
// maximum wait, and failure backoff before LIMIT, matching OllamaCloudUsageIsAutoRefreshDue.
// More than twenty active but not-yet-due groups must not starve a group past its maximum wait.
func TestListDueOllamaCloudUsageProvidersSQLDueRulesMatchService(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	debounce := time.Minute
	maxWait := time.Hour

	createOK := func(name string, fetched, lastUsed time.Time) *providercore.Record {
		t.Helper()
		return mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: name, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": name, "base_url": "https://ollama.com"},
			Extra: map[string]any{
				providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
				providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
				providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{
					"status":          providercore.OllamaCloudUsageStatusOK,
					"fetched_at":      fetched.UTC().Format(time.RFC3339Nano),
					"last_attempt_at": fetched.UTC().Format(time.RFC3339Nano),
					"next_refresh_at": fetched.Add(maxWait).UTC().Format(time.RFC3339Nano),
				},
			},
			LastUsedAt: &lastUsed,
		})
	}
	createFailed := func(name string, lastAttempt, lastUsed, nextRefresh time.Time, nextRefreshRaw string) *providercore.Record {
		t.Helper()
		snapshot := map[string]any{
			"status":          providercore.OllamaCloudUsageStatusFailed,
			"last_attempt_at": lastAttempt.UTC().Format(time.RFC3339Nano),
			"failure_count":   1,
		}
		if nextRefreshRaw != "" {
			snapshot["next_refresh_at"] = nextRefreshRaw
		} else {
			snapshot["next_refresh_at"] = nextRefresh.UTC().Format(time.RFC3339Nano)
		}
		return mustCreateProvider(t, tx.Client(), &providercore.Record{
			Name: name, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": name, "base_url": "https://ollama.com"},
			Extra: map[string]any{
				providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=fixture",
				providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
				providercore.OllamaCloudUsageSnapshotExtraKey:    snapshot,
			},
			LastUsedAt: &lastUsed,
		})
	}

	// Twenty-one recently active groups exceed the twenty-row limit.
	// They must be filtered before LIMIT so due groups can still refresh.
	notDueIDs := make(map[int64]struct{}, 21)
	for i := range 21 {
		// Fetched ten minutes ago and used ten seconds ago: due_at is now+50s, so it is not due.
		acc := createOK(fmt.Sprintf("ollama-not-due-debounce-%02d", i), now.Add(-10*time.Minute), now.Add(-10*time.Second))
		notDueIDs[acc.ID] = struct{}{}
	}

	// Maximum wait makes this group due despite activity ten seconds ago.
	// due_at = min(now-10s+1m, now-2h+1h) = now-1h.
	maxWaitDue := createOK("ollama-due-maxwait", now.Add(-2*time.Hour), now.Add(-10*time.Second))

	// Activity two minutes ago is past the one-minute debounce.
	debounceDue := createOK("ollama-due-debounce", now.Add(-30*time.Minute), now.Add(-2*time.Minute))

	// Recent activity remains within the debounce period.
	_ = createOK("ollama-not-due-fresh", now.Add(-30*time.Minute), now.Add(-20*time.Second))

	// New activity after a failure still respects next_refresh_at backoff.
	_ = createFailed("ollama-fail-backoff", now.Add(-30*time.Minute), now.Add(-2*time.Minute), now.Add(10*time.Minute), "")

	// New activity after backoff expires makes this group due.
	failDue := createFailed("ollama-fail-due", now.Add(-30*time.Minute), now.Add(-2*time.Minute), now.Add(-time.Minute), "")

	// Invalid next_refresh_at fails open to activity-based eligibility without aborting the query.
	failInvalidNext := createFailed("ollama-fail-invalid-next", now.Add(-30*time.Minute), now.Add(-2*time.Minute), time.Time{}, "not-a-timestamp")

	providers, err := repo.ListDueOllamaCloudUsageProviders(ctx, now, debounce, maxWait, 20)
	require.NoError(t, err)

	ids := providerIDs(providers)
	require.Contains(t, ids, maxWaitDue.ID, "max-wait due group must not be starved by not-yet-due activity groups")
	require.Contains(t, ids, debounceDue.ID, "success debounce elapsed must be due in SQL")
	require.Contains(t, ids, failDue.ID, "failure after backoff with new activity must be due in SQL")
	require.Contains(t, ids, failInvalidNext.ID, "invalid next_refresh_at must fail open to activity due")
	require.LessOrEqual(t, len(providers), 20)

	// These fixtures follow OllamaCloudUsageIsAutoRefreshDue:
	// not-yet-due groups stay out even when their count exceeds the result limit.
	for _, id := range ids {
		_, isNotDue := notDueIDs[id]
		require.False(t, isNotDue, "not-yet-due debounce group %d must not be returned by SQL LIMIT layer", id)
	}
	require.NotContains(t, ids, int64(0))

	// Exclude both recently successful groups and failed groups still in backoff.
	for _, provider := range providers {
		require.NotContains(t, provider.Name, "not-due")
		require.NotEqual(t, "ollama-fail-backoff", provider.Name)
		require.NotEqual(t, "ollama-not-due-fresh", provider.Name)
	}
}
