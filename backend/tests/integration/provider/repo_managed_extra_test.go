package provider_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// deprecatedOpenAILongContextBillingExtraKey identifies the retired OpenAI long-context billing toggle.
const deprecatedOpenAILongContextBillingExtraKey = "openai_long_context_billing_enabled"

func TestUpdateCNUsageMonitorSnapshotCASWritesSnapshotAndOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	expectedUpdatedAt := time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)WITH updated AS \(.*updated_at = \$5.*INSERT INTO scheduler_outbox`).
		WithArgs("", providercore.CNUsageMonitorSnapshotExtraKey, sqlmock.AnyArg(), int64(27), expectedUpdatedAt, scheduler.SchedulerOutboxEventProviderChanged).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := newProviderStoreContract(client, db, nil)
	written, err := repo.UpdateCNUsageMonitorSnapshotCAS(context.Background(), 27, expectedUpdatedAt, &providercore.CNUsageMonitorSnapshot{
		Version:       1,
		Adapter:       providercore.UpstreamUsageAdapterKimiBalance,
		IdentityHash:  "identity",
		LastAttemptAt: expectedUpdatedAt,
	}, "")
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateCNUsageMonitorSnapshotCASRejectsStaleUpdatedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	expectedUpdatedAt := time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)WITH updated AS \(.*updated_at = \$5.*INSERT INTO scheduler_outbox`).
		WithArgs("", providercore.CNUsageMonitorSnapshotExtraKey, sqlmock.AnyArg(), int64(27), expectedUpdatedAt, scheduler.SchedulerOutboxEventProviderChanged).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	repo := newProviderStoreContract(client, db, nil)
	written, err := repo.UpdateCNUsageMonitorSnapshotCAS(context.Background(), 27, expectedUpdatedAt, &providercore.CNUsageMonitorSnapshot{
		Version:       1,
		Adapter:       providercore.UpstreamUsageAdapterKimiBalance,
		IdentityHash:  "stale",
		LastAttemptAt: expectedUpdatedAt,
	}, "")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLockAndMergeProviderManagedExtraProtectsOllamaFields(t *testing.T) {
	tests := []struct {
		name                 string
		groupIdentityMatches bool
		proxyIdentityMatches bool
		wantSession          bool
		wantSnapshot         bool
	}{
		{name: "身份与代理未变时保留全部状态", groupIdentityMatches: true, proxyIdentityMatches: true, wantSession: true, wantSnapshot: true},
		{name: "代理变化时保留会话并清理快照", groupIdentityMatches: true, proxyIdentityMatches: false, wantSession: true, wantSnapshot: false},
		{name: "凭据变化时清理全部状态", groupIdentityMatches: false, proxyIdentityMatches: true, wantSession: false, wantSnapshot: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })

			mock.ExpectQuery(`(?s)`+regexp.QuoteMeta("SELECT")+`.*`+regexp.QuoteMeta("FOR NO KEY UPDATE")).
				WithArgs(int64(29), capability.PlatformAnthropic, capability.ProviderTypeAPIKey, `{"api_key":"key","base_url":"https://ollama.com"}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"ollama_group_unchanged", "ollama_proxy_unchanged", "ollama_session", "ollama_auto", "ollama_snapshot"}).
					AddRow(tt.groupIdentityMatches, tt.proxyIdentityMatches, []byte(`"local-ciphertext"`), []byte(`true`), []byte(`{"status":"ok"}`)))

			provider := &providercore.Record{
				ID: 29, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
				Credentials: map[string]any{"api_key": "key", "base_url": "https://ollama.com"},
				Extra: map[string]any{
					providercore.OllamaCloudUsageSessionExtraKey:     "forged-ciphertext",
					providercore.OllamaCloudUsageAutoRefreshExtraKey: false,
					providercore.OllamaCloudUsageSnapshotExtraKey:    map[string]any{"status": "forged"},
					deprecatedUpstreamBillingProbeExtraKey:           map[string]any{"status": "stale"},
				},
			}
			got, err := newProviderStoreContract(client, nil, nil).LockAndMergeProviderManagedExtra(context.Background(), client, provider)
			require.NoError(t, err)
			require.NotContains(t, got, deprecatedUpstreamBillingProbeExtraKey)
			if tt.wantSession {
				require.Equal(t, "local-ciphertext", got[providercore.OllamaCloudUsageSessionExtraKey])
				require.Equal(t, true, got[providercore.OllamaCloudUsageAutoRefreshExtraKey])
			} else {
				require.NotContains(t, got, providercore.OllamaCloudUsageSessionExtraKey)
				require.NotContains(t, got, providercore.OllamaCloudUsageAutoRefreshExtraKey)
			}
			if tt.wantSnapshot {
				require.Equal(t, map[string]any{"status": "ok"}, got[providercore.OllamaCloudUsageSnapshotExtraKey])
			} else {
				require.NotContains(t, got, providercore.OllamaCloudUsageSnapshotExtraKey)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUpdateExtraDiscardsDeprecatedProviderExtraKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers SET extra = .* - 'upstream_billing_probe' - 'upstream_billing_probe_enabled' - 'openai_long_context_billing_enabled'.*`).
		WithArgs(`{"custom":"value"}`, int64(27)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(scheduler.SchedulerOutboxEventProviderChanged, int64(27), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newProviderStoreContract(client, db, nil)

	err = repo.UpdateExtra(context.Background(), 27, map[string]any{
		deprecatedUpstreamBillingProbeExtraKey:        map[string]any{"status": "forged"},
		deprecatedUpstreamBillingProbeEnabledExtraKey: true,
		deprecatedOpenAILongContextBillingExtraKey:    []bool{true},
		"custom": "value",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkUpdateDiscardsDeprecatedLongContextBillingExtra(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers SET extra = .* - 'openai_long_context_billing_enabled'.*`).
		WithArgs([]byte(`{"custom":"value"}`), `{27}`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newProviderStoreContract(client, db, nil)
	extra := map[string]any{
		deprecatedOpenAILongContextBillingExtraKey: map[string]any{"malformed": true},
		"custom": "value",
	}

	rows, err := repo.BulkUpdate(context.Background(), []int64{27}, providercore.ProviderBulkUpdate{Extra: extra})

	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NotContains(t, extra, deprecatedOpenAILongContextBillingExtraKey)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateExtraRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers SET extra = .*`).
		WithArgs(`{"custom":"value"}`, int64(27)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newProviderStoreContract(client, db, nil)
	err = repo.UpdateExtra(context.Background(), 27, map[string]any{"custom": "value"})

	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateCredentialsRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers.*credentials IS DISTINCT FROM \$1::jsonb.*- 'upstream_billing_probe'.*- 'ollama_cloud_usage_snapshot'`).
		WithArgs(`{"api_key":"sk-new"}`, int64(27)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newProviderStoreContract(client, db, nil)
	err = repo.UpdateCredentials(context.Background(), 27, map[string]any{"api_key": "sk-new"})

	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkUpdateRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	name := "renamed"
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE providers SET name = \$1.*WHERE id = ANY\(\$2\)`).
		WithArgs(name, `{27,28}`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newProviderStoreContract(client, db, nil)
	rows, err := repo.BulkUpdate(context.Background(), []int64{27, 28}, providercore.ProviderBulkUpdate{Name: &name})

	require.EqualError(t, err, "outbox failed")
	require.Zero(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}
