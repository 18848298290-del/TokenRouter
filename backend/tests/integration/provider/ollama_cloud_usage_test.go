package provider_test

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Ollama Cloud managed writes compare loaded provider and proxy identities
// before replacing group state. SQL mocks check lock and transaction sequences,
// parameterized ciphertext, due-query predicates, and credential-cleanup
// clauses; their assertions describe emitted SQL and configured results rather
// than database execution.

// newOllamaCloudUsageRepositoryTestClient returns an Ent client and SQL mock
// using the PostgreSQL dialect. Callers configure expectations and check them
// before cleanup. Both cleanup callbacks reach the same connection pool; the
// client callback runs first, and close errors are ignored.
func newOllamaCloudUsageRepositoryTestClient(t *testing.T) (*dbent.Client, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return client, mock
}

// ollamaCloudUsageRepositoryProvider returns a fixed eligible provider with
// encrypted session fixture data.
func ollamaCloudUsageRepositoryProvider() *providercore.Record {
	return &providercore.Record{
		ID: 17, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://ollama.com"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "cipher:wos-session=secret",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
		},
	}
}

// TestUpdateOllamaCloudUsageSnapshotRowsAffectedZeroIsIdentityConflict returns
// a matching locked group followed by a zero-row managed update. The repository
// reports an identity conflict and requests rollback according to the SQL mock
// sequence.
func TestUpdateOllamaCloudUsageSnapshotRowsAffectedZeroIsIdentityConflict(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	mock.ExpectBegin()
	expectOllamaCloudUsageGroupLock(mock, ollamaCloudUsageRepositoryProvider(), true,
		`"cipher:wos-session=secret"`, `true`, `null`)
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE providers")).
		WithArgs(sqlmock.AnyArg(), "key", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	repo := newProviderStoreContract(client, nil, nil)

	err := repo.UpdateOllamaCloudUsageSnapshot(context.Background(), ollamaCloudUsageRepositoryProvider(), &providercore.OllamaCloudUsageSnapshot{
		Status:        providercore.OllamaCloudUsageStatusOK,
		LastAttemptAt: time.Now(),
		NextRefreshAt: time.Now().Add(time.Hour),
	})

	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageIdentityChanged)
	require.NoError(t, mock.ExpectationsWereMet())
}

// expectOllamaCloudUsageGroupLock registers a matching group-lock query for the
// provider's API key and complete anchor identity. The configured row supplies
// managed JSON values and anchorMatches; credentials normalize nil to an empty
// object. A non-string API key supplies an empty query argument.
func expectOllamaCloudUsageGroupLock(
	mock sqlmock.Sqlmock,
	provider *providercore.Record,
	anchorMatches bool,
	sessionJSON, autoJSON, snapshotJSON string,
) {
	apiKey, _ := provider.Credentials["api_key"].(string)
	// The lock query compares encoded credentials, so marshal fixture values here;
	// an encoding error leaves an empty expected argument for the mock query.
	credentials, _ := json.Marshal(normalizeJSONMap(provider.Credentials))
	var proxyID any
	if provider.ProxyID != nil {
		proxyID = *provider.ProxyID
	}
	mock.ExpectQuery(`(?s)`+regexp.QuoteMeta("SELECT")+`.*`+regexp.QuoteMeta("FOR NO KEY UPDATE")).
		WithArgs(apiKey, provider.ID, provider.Platform, provider.Type, string(credentials), proxyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "anchor_matches", "session", "auto_refresh", "snapshot"}).
			AddRow(provider.ID, anchorMatches, sessionJSON, autoJSON, snapshotJSON))
}

// TestOllamaCloudUsageManagedWriteRejectsChangedProxyIdentity supplies a locked
// proxy with a host different from the loaded provider. The session save
// returns an identity-conflict error and rolls back before group updates.
func TestOllamaCloudUsageManagedWriteRejectsChangedProxyIdentity(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "new.example", 3128, "user", "pass", providercore.StatusActive))
	mock.ExpectRollback()

	provider := ollamaCloudUsageRepositoryProvider()
	proxyID := int64(9)
	provider.ProxyID = &proxyID
	provider.Proxy = &egress.Proxy{
		ID: proxyID, Protocol: "http", Host: "old.example", Port: 3128,
		Username: "user", Password: "pass", Status: providercore.StatusActive,
	}
	repo := newProviderStoreContract(client, nil, nil)

	err := repo.SaveOllamaCloudUsageSession(context.Background(), provider, "cipher:wos-session=replacement", true)

	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageIdentityChanged)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSaveAndDeleteOllamaCloudUsageSessionKeepCiphertextOutOfSQL captures SQL
// text while mocking successful session save and deletion. Ciphertext travels
// in arguments, and the secret value is absent from every captured query.
func TestSaveAndDeleteOllamaCloudUsageSessionKeepCiphertextOutOfSQL(t *testing.T) {
	var capturedSQL []string
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		capturedSQL = append(capturedSQL, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newProviderStoreContract(client, db, nil)
	provider := ollamaCloudUsageRepositoryProvider()
	const replacement = "cipher:wos-session=browser-cookie-secret"

	mock.ExpectBegin()
	expectOllamaCloudUsageGroupLock(mock, provider, true, `"cipher:wos-session=secret"`, `true`, `null`)
	mock.ExpectExec(`(?s)UPDATE providers.*ollama_cloud_usage_session.*ollama_cloud_usage_auto_refresh.*ollama_cloud_usage_snapshot`).
		WithArgs(`{"ollama_cloud_usage_auto_refresh":true,"ollama_cloud_usage_session":"cipher:wos-session=browser-cookie-secret"}`, "key", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.SaveOllamaCloudUsageSession(context.Background(), provider, replacement, true))

	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = replacement
	mock.ExpectBegin()
	expectOllamaCloudUsageGroupLock(mock, provider, true, `"cipher:wos-session=browser-cookie-secret"`, `true`, `null`)
	mock.ExpectExec(`(?s)UPDATE providers.*ollama_cloud_usage_session.*ollama_cloud_usage_auto_refresh.*ollama_cloud_usage_snapshot`).
		WithArgs(`{}`, "key", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.DeleteOllamaCloudUsageSession(context.Background(), provider))

	require.NotEmpty(t, capturedSQL)
	for _, query := range capturedSQL {
		require.NotContains(t, query, "browser-cookie-secret")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOllamaCloudBaseURLSQLRegexMatchesServiceSemantics compares the exported
// SQL regex evaluated by Go regexp with service eligibility for five Base URL
// forms. Host variants and path case produce the same boolean classification
// for those fixture inputs.
func TestOllamaCloudBaseURLSQLRegexMatchesServiceSemantics(t *testing.T) {
	for _, baseURL := range []string{
		"https://ollama.com",
		"HTTPS://WWW.OLLAMA.COM:443/v1",
		"https://ollama.com/V1",
		"https://ollama.com/v1/",
		"https://ollama.com.evil.test/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			matched, err := regexp.MatchString(providerpostgres.OllamaCloudBaseURLRegexSQL, baseURL)
			require.NoError(t, err)
			provider := ollamaCloudUsageRepositoryProvider()
			provider.Credentials["base_url"] = baseURL
			require.Equal(t, providercore.IsOllamaCloudUsageProvider(provider), matched)
		})
	}
}

// TestListOllamaCloudUsageGroupProvidersUsesOneStrictBatchQuery supplies two
// same-key providers using different supported platforms and URL forms. One
// empty mocked query contains the expected API-key, platform, and strict URL
// predicates.
func TestListOllamaCloudUsageGroupProvidersUsesOneStrictBatchQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var capturedSQL string
	mock.ExpectQuery("SELECT id").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newProviderStoreContract(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)
	first := ollamaCloudUsageRepositoryProvider()
	second := ollamaCloudUsageRepositoryProvider()
	second.ID = 18
	second.Platform = capability.PlatformAnthropic
	second.Credentials = map[string]any{"api_key": "key", "base_url": "https://www.ollama.com:443/v1"}

	providers, err := repo.ListOllamaCloudUsageGroupProviders(context.Background(), []*providercore.Record{first, second})

	require.NoError(t, err)
	require.Empty(t, providers)
	query := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, query, "credentials ->> 'api_key' = ANY($1)")
	require.Contains(t, query, "platform IN ('openai', 'anthropic')")
	require.Contains(t, query, "jsonb_typeof(credentials -> 'api_key') = 'string'")
	require.Contains(t, query, providerpostgres.OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'"))
	require.NotContains(t, query, "~*")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestListDueOllamaCloudUsageProvidersFiltersOrdersAndLimits captures the
// automatic-refresh query and its time arguments. Expected eligibility,
// activity aggregation, parsing, due ordering, and result-limit clauses appear
// in the emitted SQL.
func TestListDueOllamaCloudUsageProvidersFiltersOrdersAndLimits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	debounce := time.Minute
	maxWait := time.Hour
	var capturedSQL string
	mock.ExpectQuery("WITH eligible AS").
		WithArgs(now.UTC(), debounce.Seconds(), maxWait.Seconds(), 20, providercore.OllamaCloudUsageMinFetchInterval.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_last_used_at"}))
	repo := newProviderStoreContract(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	providers, err := repo.ListDueOllamaCloudUsageProviders(context.Background(), now, debounce, maxWait, 20)

	require.NoError(t, err)
	require.Empty(t, providers)
	normalized := normalizeSQLWhitespace(capturedSQL)
	for _, clause := range []string{
		"deleted_at IS NULL",
		"status = 'active'",
		"platform IN ('openai', 'anthropic')",
		"type = 'apikey'",
		providerpostgres.OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'"),
		"jsonb_typeof(extra -> 'ollama_cloud_usage_session') = 'string'",
		`extra @> '{"ollama_cloud_usage_auto_refresh": true}'::jsonb`,
		"MAX(last_used_at) AS group_last_used_at",
		"PARTITION BY api_key",
		"WHERE group_rank = 1",
		"LIMIT $4",
		"make_interval(secs => $2::double precision)",
		"make_interval(secs => $3::double precision)",
		// The due query receives the service's minimum successful-fetch interval,
		// keeping recent successful observations out even when later activity exists.
		"make_interval(secs => $5::double precision)",
		// Normalize UTC notation and fractional precision before JSONPath parsing;
		// a parse failure becomes NULL and would admit extra candidates through due filtering.
		`regexp_replace( regexp_replace( fetched_at, '(\.[0-9]{6})[0-9]+(Z|[+-][0-9]{2}:[0-9]{2})$', '\1\2' ), 'Z$', '+00:00' )`,
		"group_last_used_at > parsed_fetched_at::timestamptz",
		"group_last_used_at > parsed_last_attempt_at::timestamptz",
		"$1 >= activity_due_at",
		"COALESCE(parsed_next_refresh_at::timestamptz, '-infinity'::timestamptz)",
		"ORDER BY due_class, due_at NULLS FIRST, id",
	} {
		require.Contains(t, normalized, clause)
	}
	require.NotContains(t, normalized, "~*")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBulkUpdateOllamaIdentityCleanupIsValueConditional records a bulk Base URL
// patch through a synthetic SQL executor. The statement conditionally removes
// managed state, and its input payload contains no submitted usage snapshot.
func TestBulkUpdateOllamaIdentityCleanupIsValueConditional(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newProviderStoreContract(nil, exec, nil)

	_, err := repo.BulkUpdate(context.Background(), []int64{17}, providercore.ProviderBulkUpdate{
		Credentials: map[string]any{"base_url": "https://www.ollama.com:443/v1"},
	})

	require.NoError(t, err)
	require.NotEmpty(t, exec.execQueries)
	query := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, query, "NOT ("+providerpostgres.OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'"))
	require.Contains(t, query, providerpostgres.OllamaCloudBaseURLMatchesSQL("$1::jsonb ->> 'base_url'"))
	require.NotContains(t, query, "~*")
	require.Contains(t, query, "platform IN ('openai', 'anthropic') AND type = 'apikey'")
	require.Contains(t, query, "- 'ollama_cloud_usage_session' - 'ollama_cloud_usage_auto_refresh' - 'ollama_cloud_usage_snapshot'")
	payload, ok := exec.execArgs[0][0].([]byte)
	require.True(t, ok)
	require.NotContains(t, string(payload), providercore.OllamaCloudUsageSnapshotExtraKey)
}

// TestUpdateCredentialsIdentityChangeClearsAllOllamaManagedExtra mocks a
// credential replacement followed by its event and commit. The expected update
// includes the API-key comparison and removal of all three managed Ollama
// fields.
func TestUpdateCredentialsIdentityChangeClearsAllOllamaManagedExtra(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers.*credentials -> 'api_key' IS DISTINCT FROM.*ollama_cloud_usage_session.*ollama_cloud_usage_auto_refresh.*ollama_cloud_usage_snapshot`).
		WithArgs(`{"api_key":"new-key","base_url":"https://ollama.com"}`, int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(scheduler.SchedulerOutboxEventProviderChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newProviderStoreContract(client, nil, nil)

	err := repo.UpdateCredentials(context.Background(), 17, map[string]any{
		"api_key": "new-key", "base_url": "https://ollama.com",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDisableOllamaCloudUsageAutoRefreshUsesGroupIdentityCAS mocks a matching
// group identity and managed state before disabling refresh. The committed
// payload retains the session ciphertext and writes auto-refresh false.
func TestDisableOllamaCloudUsageAutoRefreshUsesGroupIdentityCAS(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	provider := ollamaCloudUsageRepositoryProvider()
	mock.ExpectBegin()
	expectOllamaCloudUsageGroupLock(mock, provider, true, `"cipher:wos-session=secret"`, `true`, `null`)
	mock.ExpectExec(`(?s)UPDATE providers.*ollama_cloud_usage_auto_refresh`).
		WithArgs(`{"ollama_cloud_usage_auto_refresh":false,"ollama_cloud_usage_session":"cipher:wos-session=secret"}`, "key", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	repo := newProviderStoreContract(client, nil, nil)

	err := repo.DisableOllamaCloudUsageAutoRefresh(context.Background(), provider)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUpdateCredentialsCleanupBranchRequiresChangedCredentials inspects the SQL
// branch guard used when replacing credentials. Its expectation requires
// complete credentials to differ before identity-based managed-state cleanup
// can qualify.
func TestUpdateCredentialsCleanupBranchRequiresChangedCredentials(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers.*CASE.*AND credentials IS DISTINCT FROM \$1::jsonb\s+AND \(\s+credentials -> 'api_key' IS DISTINCT FROM`).
		WithArgs(`{"api_key":"same-key","base_url":"https://relay.example.com/v1"}`, int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(scheduler.SchedulerOutboxEventProviderChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newProviderStoreContract(client, nil, nil)

	err := repo.UpdateCredentials(context.Background(), 17, map[string]any{
		"api_key": "same-key", "base_url": "https://relay.example.com/v1",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// normalizeJSONMap returns the input map, replacing nil with an empty object.
// Nonnil maps remain shared; callers use the result for JSON encoding rather
// than mutating a private copy.
func normalizeJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
