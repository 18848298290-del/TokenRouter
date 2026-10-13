package provider_test

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Conditional provider mutations include identity and eligibility predicates
// alongside scheduling event writes. Recorded SQL and mock rows check those
// clauses, zero-row outcomes, cancellation after a successful statement, and
// OAuth refresh candidate filtering without executing the statements on
// PostgreSQL.

// TestProviderRepository_SetTempUnschedulable_NoRowsAffectedDoesNotWriteOutbox
// supplies a zero-row result through the SQL recorder. The repository completes
// successfully after one provider update and emits no scheduling statement.
func TestProviderRepository_SetTempUnschedulable_NoRowsAffectedDoesNotWriteOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newProviderStoreContract(nil, exec, nil)
	until := time.Now().Add(10 * time.Minute)

	err := repo.SetTempUnschedulable(context.Background(), 42, until, "retry")
	require.NoError(t, err)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "UPDATE providers")
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

// TestProviderRepository_ResetQuotaUsedAndClearRateLimitCooldown_NoRowsAffectedReturnsNotFoundWithoutOutbox
// supplies a zero-row quota-reset result through the recorder. The usage store
// returns provider-not-found after one update and emits no outbox statement.
func TestProviderRepository_ResetQuotaUsedAndClearRateLimitCooldown_NoRowsAffectedReturnsNotFoundWithoutOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := billingpostgres.NewProviderUsageStore(exec, billingpostgres.ProviderUsageOptions{})

	err := repo.ResetQuotaUsedAndClearRateLimitCooldown(context.Background(), 42)

	require.ErrorIs(t, err, provider.ErrProviderNotFound)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "UPDATE providers")
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

// TestProviderRepository_GrokCredentialConditionalMutationsAreEligibleAndAtomicallyPropagated
// records permanent and transient Grok SQL using zero affected rows. Each
// statement includes scheduling eligibility, exact credentials, nullable proxy
// matching, and its conditional event insertion.
func TestProviderRepository_GrokCredentialConditionalMutationsAreEligibleAndAtomicallyPropagated(t *testing.T) {
	proxyID := int64(77)
	snapshot := provider.CredentialMutationSnapshot{
		CredentialsJSON: `{"access_token":"access","refresh_token":"refresh","_token_version":123}`,
		ProxyID:         &proxyID,
	}

	t.Run("permanent", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
		repo := newProviderStoreContract(nil, exec, nil)

		updated, err := repo.SetGrokCredentialErrorIfMatch(context.Background(), 42, snapshot, "revoked", string(forwardcore.GrokCredentialReasonProxyInvalid))

		require.NoError(t, err)
		require.False(t, updated)
		require.Len(t, exec.execQueries, 1)
		normalized := normalizeSQLWhitespace(exec.execQueries[0])
		require.Contains(t, normalized, "WITH updated AS ( UPDATE providers AS a")
		require.Contains(t, normalized, "a.schedulable IS TRUE")
		require.Contains(t, normalized, "a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW()")
		require.Contains(t, normalized, "a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW()")
		require.Contains(t, normalized, "a.overload_until IS NULL OR a.overload_until <= NOW()")
		require.Contains(t, normalized, "a.credentials = $7::jsonb")
		require.Contains(t, normalized, "a.proxy_id IS NOT DISTINCT FROM $8")
		require.Contains(t, normalized, "NOT EXISTS ( SELECT 1 FROM proxies p")
		require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
		require.Len(t, exec.execArgs[0], 10)
		require.Equal(t, snapshot.CredentialsJSON, exec.execArgs[0][6])
		require.Equal(t, &proxyID, exec.execArgs[0][7])
		require.Equal(t, string(forwardcore.GrokCredentialReasonProxyInvalid), exec.execArgs[0][8])
		require.Equal(t, scheduler.SchedulerOutboxEventProviderChanged, exec.execArgs[0][9])
	})

	t.Run("transient", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
		repo := newProviderStoreContract(nil, exec, nil)

		updated, err := repo.SetGrokCredentialTempUnschedulableIfMatch(
			context.Background(), 42, snapshot, time.Now().Add(time.Minute), "temporary",
		)

		require.NoError(t, err)
		require.False(t, updated)
		require.Len(t, exec.execQueries, 1)
		normalized := normalizeSQLWhitespace(exec.execQueries[0])
		require.Contains(t, normalized, "WITH updated AS ( UPDATE providers AS a")
		require.Contains(t, normalized, "a.schedulable IS TRUE")
		require.Contains(t, normalized, "a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW()")
		require.Contains(t, normalized, "a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW()")
		require.Contains(t, normalized, "a.overload_until IS NULL OR a.overload_until <= NOW()")
		require.Contains(t, normalized, "a.credentials = $7::jsonb")
		require.Contains(t, normalized, "a.proxy_id IS NOT DISTINCT FROM $8")
		require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
		require.Len(t, exec.execArgs[0], 9)
		require.Equal(t, snapshot.CredentialsJSON, exec.execArgs[0][6])
		require.Equal(t, &proxyID, exec.execArgs[0][7])
		require.Equal(t, scheduler.SchedulerOutboxEventProviderChanged, exec.execArgs[0][8])
	})
}

// TestProviderRepository_GrokCredentialCommitCarriesOutboxAcrossCallerCancellation
// cancels the context inside the recorder's success callback before ExecContext
// returns its configured result. Each path reports applied and includes the
// event insert in the same recorded statement.
func TestProviderRepository_GrokCredentialCommitCarriesOutboxAcrossCallerCancellation(t *testing.T) {
	snapshot := provider.CredentialMutationSnapshot{CredentialsJSON: `{"access_token":"access","refresh_token":"refresh"}`}
	tests := []struct {
		name string // mutation scenario label
		// mutate invokes the selected conditional Grok database mutation.
		mutate func(context.Context, *providerpostgres.ProviderStore) (bool, error)
	}{
		{
			name: "permanent",
			mutate: func(ctx context.Context, repo *providerpostgres.ProviderStore) (bool, error) {
				return repo.SetGrokCredentialErrorIfMatch(ctx, 42, snapshot, string(forwardcore.GrokCredentialReasonRevoked), string(forwardcore.GrokCredentialReasonProxyInvalid))
			},
		},
		{
			name: "transient",
			mutate: func(ctx context.Context, repo *providerpostgres.ProviderStore) (bool, error) {
				return repo.SetGrokCredentialTempUnschedulableIfMatch(ctx, 42, snapshot, time.Now().Add(time.Minute), string(forwardcore.GrokCredentialReasonRefreshTransient))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			// Cancel within the successful recorder callback before ExecContext returns;
			// this places caller cancellation before the mutation consumes the configured
			// result, while its SQL text still contains the combined event write.
			exec := &recordingSQLExecutor{result: rowsAffectedResult(1), afterExec: cancel}
			repo := newProviderStoreContract(nil, exec, nil)

			updated, err := tt.mutate(ctx, repo)

			require.NoError(t, err)
			require.True(t, updated)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.Len(t, exec.execQueries, 1, "state update and scheduler outbox must share one atomic SQL statement")
			require.Contains(t, normalizeSQLWhitespace(exec.execQueries[0]), "INSERT INTO scheduler_outbox")
		})
	}
}

// TestProviderRepository_SetGrokOAuthErrorIfCredentialsUnchanged_RequiresActiveExactCredentialMatch
// records missing-refresh-token reconciliation with zero affected rows. Its
// single statement compares active OAuth identity and complete credentials,
// while limiting reconciliation to absent or blank refresh tokens.
func TestProviderRepository_SetGrokOAuthErrorIfCredentialsUnchanged_RequiresActiveExactCredentialMatch(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newProviderStoreContract(nil, exec, nil)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		context.Background(),
		42,
		map[string]any{"access_token": "observed", "_token_version": int64(7)},
		"missing refresh token",
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1, "the provider mutation and conditional outbox insert must be one statement")
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH updated AS")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Contains(t, normalized, "FROM updated")
	require.Contains(t, normalized, "platform = $4")
	require.Contains(t, normalized, "type = $5")
	require.Contains(t, normalized, "status = $6")
	require.Contains(t, normalized, "credentials = $7::jsonb")
	require.Contains(t, normalized, "NULLIF(BTRIM(a.credentials->>'refresh_token'), '') IS NULL")
	require.Len(t, exec.execArgs, 1)
	require.Equal(t, billing.StatusActive, exec.execArgs[0][5])
	require.Contains(t, exec.execArgs[0][6], `"_token_version":7`)
}

// TestProviderRepository_SetGrokOAuthErrorIfCredentialsUnchanged_AppliedWritesOutbox
// records a reconciliation statement with one affected row. The repository
// reports applied, and the SQL inserts the provider event from that statement's
// updated rows.
func TestProviderRepository_SetGrokOAuthErrorIfCredentialsUnchanged_AppliedWritesOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newProviderStoreContract(nil, exec, nil)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		context.Background(),
		42,
		map[string]any{"access_token": "observed"},
		"missing refresh token",
	)

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH updated AS")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Contains(t, normalized, "SELECT $8, updated.id, NULL, NULL FROM updated")
}

// TestProviderRepository_SetGrokOAuthRefreshErrorIfCredentialsUnchanged_UsesAttemptCredentialsAndProxy
// records an error for attempted refresh credentials and a selected proxy. The
// statement matches both observations and permits the supplied nonempty refresh
// token.
func TestProviderRepository_SetGrokOAuthRefreshErrorIfCredentialsUnchanged_UsesAttemptCredentialsAndProxy(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newProviderStoreContract(nil, exec, nil)
	proxyID := int64(17)

	applied, err := repo.SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
		context.Background(),
		42,
		map[string]any{"refresh_token": "attempted", "_token_version": int64(7)},
		&proxyID,
		"revoked",
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "credentials = $7::jsonb")
	require.Contains(t, normalized, "proxy_id IS NOT DISTINCT FROM $8")
	require.NotContains(t, normalized, "credentials->>'refresh_token'",
		"background invalid_grant CAS must accept the attempted refresh token; only reconciliation requires it missing")
	require.Equal(t, &proxyID, exec.execArgs[0][7])
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Len(t, exec.execArgs[0], 9)
}

// TestProviderRepository_SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged_UsesAttemptCredentialsAndProxy
// records a transient refresh failure with zero affected rows. SQL compares the
// attempted credentials and proxy, qualifies extension of the temporary window,
// and includes a conditional scheduling event.
func TestProviderRepository_SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged_UsesAttemptCredentialsAndProxy(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newProviderStoreContract(nil, exec, nil)
	proxyID := int64(19)

	applied, err := repo.SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
		context.Background(),
		42,
		map[string]any{"refresh_token": "attempted", "_token_version": int64(8)},
		&proxyID,
		time.Now().Add(10*time.Minute),
		"retry exhausted",
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "credentials = $7::jsonb")
	require.Contains(t, normalized, "proxy_id IS NOT DISTINCT FROM $8")
	require.Contains(t, normalized, "a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until < $1")
	require.Len(t, exec.execArgs[0], 9)
	require.Equal(t, &proxyID, exec.execArgs[0][7])
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
}

// TestProviderRepository_UpdateGrokOAuthCredentialsIfUnchanged_UsesExactAttemptStateAndAtomicOutbox
// records an applied Grok credential rotation. One SQL statement matches the
// attempted credentials and nullable proxy, stores replacement credentials, and
// includes its scheduling event.
func TestProviderRepository_UpdateGrokOAuthCredentialsIfUnchanged_UsesExactAttemptStateAndAtomicOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newProviderStoreContract(nil, exec, nil)
	proxyID := int64(29)

	applied, err := repo.UpdateGrokOAuthCredentialsIfUnchanged(
		context.Background(),
		42,
		map[string]any{"refresh_token": "attempted", "_token_version": int64(9)},
		&proxyID,
		map[string]any{"refresh_token": "rotated", "_token_version": int64(10)},
	)

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH updated AS")
	require.Contains(t, normalized, "credentials = $1::jsonb")
	require.Contains(t, normalized, "credentials = $5::jsonb")
	require.Contains(t, normalized, "proxy_id IS NOT DISTINCT FROM $6")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Len(t, exec.execArgs[0], 7)
	require.Equal(t, &proxyID, exec.execArgs[0][5])
}

// TestProviderRepository_ListOAuthRefreshCandidatePage_SQLFilter captures the
// background-refresh query with registry platforms, cursor, and limit. Expected
// SQL admits OAuth, setup-token, and Qoder cases, excludes qualifying retry
// cooldowns, and handles nullable health state.
func TestProviderRepository_ListOAuthRefreshCandidatePage_SQLFilter(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var capturedSQL string
	var capturedArgs []any
	mock.ExpectQuery("SELECT id").
		WillReturnRows(sqlmock.NewRows([]string{"id"})).
		WillDelayFor(0)

	repo := newProviderStoreContract(nil, captureQuerySQL{db: db, captured: &capturedSQL, args: &capturedArgs}, nil)

	page, err := repo.ListOAuthRefreshCandidatePage(context.Background(), provider.OAuthRefreshPageOptions{
		Platforms:            []string{capability.PlatformAnthropic, capability.PlatformOpenAI, capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformGrok},
		AfterID:              100,
		Limit:                200,
		ActiveOnly:           true,
		IncludeSetupToken:    true,
		RequireRefreshToken:  true,
		ExcludeRetryCooldown: true,
	})
	require.NoError(t, err)
	require.Empty(t, page.Providers)

	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "schedulable = TRUE",
		"永久不可调度提供商不能继续进入 OAuth 刷新候选")
	require.Contains(t, normalized, "status = 'active'")
	// Setup-token eligibility shares the background page with OAuth, while Qoder
	// COSY has its own predicate; the query must carry both requested forms.
	require.Contains(t, normalized, "type IN ('oauth', 'setup-token')")
	require.Contains(t, normalized, "platform = 'qoder' AND type = 'cosy'")
	require.Contains(t, normalized, "platform = ANY($1)")
	require.NotContains(t, normalized, "platform IN ('anthropic'",
		"candidate platforms must come from the refresher registry instead of a second hard-coded list")
	require.Contains(t, normalized, "credentials ? 'refresh_token'")
	require.Contains(t, normalized, "btrim(credentials->>'refresh_token') <> ''")
	require.Contains(t, normalized, "temp_unschedulable_until > NOW()")
	require.Contains(t, normalized, "temp_unschedulable_reason LIKE 'token refresh retry exhausted:%'")
	require.Contains(t, normalized, "IS NOT TRUE",
		"must use IS NOT TRUE so providers with NULL temp_unschedulable_until are not silently excluded by PG 3-valued logic")
	require.NotContains(t, normalized, "AND NOT (",
		"plain NOT (...) excludes NULL temp_unschedulable_until rows (the common healthy case)")
	require.Contains(t, normalized, "id > $2")
	require.Contains(t, normalized, "ORDER BY id ASC")
	require.Contains(t, normalized, "LIMIT $3")
	require.NotContains(t, normalized, "credentials->>'expires_at'")
	require.Len(t, capturedArgs, 3)
	require.Equal(t, int64(100), capturedArgs[1])
	require.Equal(t, 200, capturedArgs[2])
	valuer, ok := capturedArgs[0].(interface {
		// Value encodes the configured platform list as a SQL argument.
		Value() (driver.Value, error)
	})
	require.True(t, ok)
	platforms, err := valuer.Value()
	require.NoError(t, err)
	require.Contains(t, platforms, capability.PlatformGrok)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProviderRepository_ListOAuthRefreshCandidatePage_ReconciliationExcludesAPIKeys
// captures a Grok reconciliation query with default options. The SQL selects
// OAuth rows in ID order and permits structurally missing refresh tokens for
// reconciliation.
func TestProviderRepository_ListOAuthRefreshCandidatePage_ReconciliationExcludesAPIKeys(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var capturedSQL string
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newProviderStoreContract(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	page, err := repo.ListOAuthRefreshCandidatePage(context.Background(), provider.OAuthRefreshPageOptions{
		Platforms: []string{capability.PlatformGrok},
		AfterID:   0,
		Limit:     50,
	})
	require.NoError(t, err)
	require.Empty(t, page.Providers)

	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "type = 'oauth'")
	require.NotContains(t, normalized, "type IN ('oauth', 'setup-token')")
	require.NotContains(t, normalized, "type = 'api-key'")
	require.NotContains(t, normalized, "credentials ? 'refresh_token'",
		"reconciliation must be able to find structurally invalid OAuth rows")
	require.Contains(t, normalized, "ORDER BY id ASC")
	require.NoError(t, mock.ExpectationsWereMet())
}
