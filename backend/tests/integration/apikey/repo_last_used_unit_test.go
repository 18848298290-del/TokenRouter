package apikey_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

// Key console reads attach the latest nonempty usage-log IP and preserve
// explicit last-used timestamps. SQLite checks those stored values and error
// paths, while a separate string assertion checks PostgreSQL lateral-query
// shape without executing or measuring that query.

// mustCreateAPIKeyRepoProvider inserts an active OpenAI API-key provider and
// returns its persisted ID. Save errors fail the test before a usage log can
// reference the fixture.
func mustCreateAPIKeyRepoProvider(t *testing.T, ctx context.Context, client *dbent.Client, name string) int64 {
	t.Helper()
	provider, err := client.Provider.Create().
		SetName(name).
		SetPlatform(capability.PlatformOpenAI).
		SetType(capability.ProviderTypeAPIKey).
		SetStatus(billing.StatusActive).
		SetCredentials(map[string]any{"api_key": "sk-test"}).
		Save(ctx)
	require.NoError(t, err)
	return provider.ID
}

// mustCreateAPIKeyRepoUsageLog persists a gpt-5 usage row with the supplied
// request ID and timestamp. A nil IP leaves that field unset, and save errors
// fail the test.
func mustCreateAPIKeyRepoUsageLog(t *testing.T, ctx context.Context, client *dbent.Client, userID, apiKeyID, providerID int64, requestID string, createdAt time.Time, ipAddress *string) {
	t.Helper()
	builder := client.UsageLog.Create().
		SetUserID(userID).
		SetAPIKeyID(apiKeyID).
		SetProviderID(providerID).
		SetRequestID(requestID).
		SetModel("gpt-5").
		SetCreatedAt(createdAt)
	if ipAddress != nil {
		builder.SetIPAddress(*ipAddress)
	}
	_, err := builder.Save(ctx)
	require.NoError(t, err)
}

// TestAPIKeyRepositoryListByUserIDAttachesLastUsedIP creates keys with usable,
// empty, and absent IP histories. Listing attaches the latest nonempty IP to
// the first key and nil to the other two.
func TestAPIKeyRepositoryListByUserIDAttachesLastUsedIP(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "list-last-used-ip@test.com")
	providerID := mustCreateAPIKeyRepoProvider(t, ctx, client, "acc-list-last-used-ip")

	withLogs := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-list-last-used-ip-logs",
		Name:   "With Logs",
		Status: billing.StatusActive,
	}
	emptyOnly := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-list-last-used-ip-empty",
		Name:   "Empty Only",
		Status: billing.StatusActive,
	}
	noLogs := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-list-last-used-ip-none",
		Name:   "No Logs",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, withLogs))
	require.NoError(t, repo.Create(ctx, emptyOnly))
	require.NoError(t, repo.Create(ctx, noLogs))

	olderIP := "198.51.100.10"
	newerEmptyIP := ""
	newestIP := "203.0.113.20"
	// Whole-second fixture times keep the SQLite timestamp comparisons
	// deterministic.
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	mustCreateAPIKeyRepoUsageLog(t, ctx, client, user.ID, withLogs.ID, providerID, "req-last-ip-older", base, &olderIP)
	mustCreateAPIKeyRepoUsageLog(t, ctx, client, user.ID, withLogs.ID, providerID, "req-last-ip-empty", base.Add(time.Hour), &newerEmptyIP)
	mustCreateAPIKeyRepoUsageLog(t, ctx, client, user.ID, withLogs.ID, providerID, "req-last-ip-newest", base.Add(2*time.Hour), &newestIP)
	mustCreateAPIKeyRepoUsageLog(t, ctx, client, user.ID, emptyOnly.ID, providerID, "req-empty-ip", base.Add(3*time.Hour), &newerEmptyIP)

	keys, _, err := repo.ListByUserID(ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 10}, apikey.APIKeyListFilters{})
	require.NoError(t, err)

	byID := make(map[int64]apikey.APIKey, len(keys))
	for _, key := range keys {
		byID[key.ID] = key
	}
	require.NotNil(t, byID[withLogs.ID].LastUsedIP)
	require.Equal(t, newestIP, *byID[withLogs.ID].LastUsedIP)
	require.Nil(t, byID[emptyOnly.ID].LastUsedIP)
	require.Nil(t, byID[noLogs.ID].LastUsedIP)
}

// TestLatestUsageLogIPsQueryPostgresUsesPerKeyLateralLookup builds SQL for two
// key IDs without executing it. Assertions require lateral per-key lookup,
// empty-IP filtering, descending tie ordering, and one argument.
func TestLatestUsageLogIPsQueryPostgresUsesPerKeyLateralLookup(t *testing.T) {
	query, args := query.KeyLatestUsageLogIPsQuery([]int64{11, 22}, dialect.Postgres)
	normalizedQuery := strings.Join(strings.Fields(query), " ")

	require.Contains(t, normalizedQuery, "FROM unnest($1::bigint[]) AS requested(api_key_id)")
	require.Contains(t, normalizedQuery, "CROSS JOIN LATERAL")
	require.Contains(t, normalizedQuery, "WHERE ul.api_key_id = requested.api_key_id")
	require.Contains(t, normalizedQuery, "AND ul.ip_address IS NOT NULL")
	require.Contains(t, normalizedQuery, "AND ul.ip_address <> ''")
	require.Contains(t, normalizedQuery, "ORDER BY ul.created_at DESC, ul.id DESC LIMIT 1")
	require.NotContains(t, normalizedQuery, "ROW_NUMBER")
	require.Len(t, args, 1)
}

// TestAPIKeyRepository_CreateWithLastUsedAt supplies an earlier timestamp when
// creating a SQLite key. The input object and subsequent repository read retain
// that timestamp within one second.
func TestAPIKeyRepository_CreateWithLastUsedAt(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "create-last-used@test.com")

	// Whole-second inputs and a one-second tolerance allow timestamp round-trip
	// comparison.
	lastUsed := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	key := &apikey.APIKey{
		UserID:     user.ID,
		Key:        "sk-create-last-used",
		Name:       "CreateWithLastUsed",
		Status:     billing.StatusActive,
		LastUsedAt: &lastUsed,
	}

	require.NoError(t, repo.Create(ctx, key))
	require.NotNil(t, key.LastUsedAt)
	require.WithinDuration(t, lastUsed, *key.LastUsedAt, time.Second)

	got, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	require.WithinDuration(t, lastUsed, *got.LastUsedAt, time.Second)
}

// TestAPIKeyRepository_UpdateLastUsed starts with a key whose last-used time is
// nil. Updating to a supplied timestamp changes both LastUsedAt and UpdatedAt
// within one second.
func TestAPIKeyRepository_UpdateLastUsed(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "update-last-used@test.com")

	key := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-update-last-used",
		Name:   "UpdateLastUsed",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	before, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Nil(t, before.LastUsedAt)

	// Whole-second inputs and a one-second tolerance compare both stored activity
	// timestamps.
	target := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
	require.NoError(t, repo.UpdateLastUsed(ctx, key.ID, target))

	after, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.NotNil(t, after.LastUsedAt)
	require.WithinDuration(t, target, *after.LastUsedAt, time.Second)
	require.WithinDuration(t, target, after.UpdatedAt, time.Second)
}

// TestAPIKeyRepository_UpdateLastUsedDeletedKey soft deletes a key before
// attempting a timestamp update. The repository reports APIKeyNotFound for that
// existing but deleted storage row.
func TestAPIKeyRepository_UpdateLastUsedDeletedKey(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "deleted-last-used@test.com")

	key := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-update-last-used-deleted",
		Name:   "UpdateLastUsedDeleted",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))
	require.NoError(t, repo.Delete(ctx, key.ID))

	err := repo.UpdateLastUsed(ctx, key.ID, time.Now().UTC())
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
}

// TestAPIKeyRepository_UpdateLastUsedDBError closes the SQLite Ent client after
// creating a key. The subsequent last-used write reports an error from the
// unavailable database dependency.
func TestAPIKeyRepository_UpdateLastUsedDBError(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "db-error-last-used@test.com")

	key := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-update-last-used-db-error",
		Name:   "UpdateLastUsedDBError",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	// Close the shared driver before the timestamp operation to make its storage
	// dependency unavailable; the registered cleanup later tolerates another
	// closure.
	require.NoError(t, client.Close())
	err := repo.UpdateLastUsed(ctx, key.ID, time.Now().UTC())
	require.Error(t, err)
}

// TestAPIKeyRepository_CreateDuplicateKey creates two objects with the same
// credential for one user. The first insertion succeeds and the second maps the
// duplicate to APIKeyExists.
func TestAPIKeyRepository_CreateDuplicateKey(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "duplicate-key@test.com")

	first := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-duplicate",
		Name:   "first",
		Status: billing.StatusActive,
	}
	second := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-duplicate",
		Name:   "second",
		Status: billing.StatusActive,
	}

	require.NoError(t, repo.Create(ctx, first))
	err := repo.Create(ctx, second)
	require.ErrorIs(t, err, apikey.ErrAPIKeyExists)
}
