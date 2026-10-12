package provider_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// proxyProviderIDsPayloadMatcher checks provider IDs and their order in an outbox payload.
type proxyProviderIDsPayloadMatcher struct {
	want []int64
}

// Match decodes a byte-slice payload and compares its provider IDs with the expected list.
func (m proxyProviderIDsPayloadMatcher) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok {
		return false
	}
	var payload struct {
		ProviderIDs []int64 `json:"provider_ids"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return reflect.DeepEqual(m.want, payload.ProviderIDs)
}

func TestProxyUpdateInvalidatesOllamaSnapshotAndEnqueuesOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR NO KEY UPDATE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "old.example", 8080, "user", "pass", billing.StatusActive))
	mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL WHERE "backup_proxy_id" = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectProxyUpdateReload(mock, 9, "new.example", "user", "pass")
	mock.ExpectQuery(`(?s)UPDATE providers.*- 'ollama_cloud_usage_snapshot'.*type = 'apikey'.*platform IN \('openai', 'anthropic'\).*extra \? 'ollama_cloud_usage_snapshot'.*RETURNING id`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)).AddRow(int64(18)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)")).
		WithArgs(scheduler.SchedulerOutboxEventProviderBulkChanged, nil, nil, proxyProviderIDsPayloadMatcher{want: []int64{17, 18}}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newProxyStoreContract(client, db)
	proxy := &egress.Proxy{
		ID: 9, Name: "proxy", Protocol: "http", Host: "new.example", Port: 8080,
		Username: "user", Password: "pass", Status: billing.StatusActive,
	}

	err = repo.Update(context.Background(), proxy)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProxyUpdateRollsBackWhenOllamaOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR NO KEY UPDATE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "old.example", 8080, "", "", billing.StatusActive))
	mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL WHERE "backup_proxy_id" = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectProxyUpdateReload(mock, 9, "new.example", "", "")
	mock.ExpectQuery(`(?s)UPDATE providers.*- 'ollama_cloud_usage_snapshot'.*type = 'apikey'.*platform IN \('openai', 'anthropic'\).*RETURNING id`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)")).
		WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newProxyStoreContract(client, db)
	proxy := &egress.Proxy{ID: 9, Name: "proxy", Protocol: "http", Host: "new.example", Port: 8080, Status: billing.StatusActive}

	err = repo.Update(context.Background(), proxy)

	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProxyUpdateSkipsOllamaInvalidationForNonIdentityChange(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR NO KEY UPDATE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "same.example", 8080, "", "", billing.StatusActive))
	mock.ExpectExec(`(?s)UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "proxies" SET "backup_proxy_id" = NULL WHERE "backup_proxy_id" = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectProxyUpdateReload(mock, 9, "same.example", "", "")
	mock.ExpectCommit()

	repo := newProxyStoreContract(client, db)
	proxy := &egress.Proxy{ID: 9, Name: "renamed", Protocol: "http", Host: "same.example", Port: 8080, Status: billing.StatusActive}

	err = repo.Update(context.Background(), proxy)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectProxyUpdateReload(mock sqlmock.Sqlmock, id int64, host, username, password string) {
	now := time.Now()
	mock.ExpectQuery(`(?s)SELECT .* FROM "proxies" WHERE "id" = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "created_at", "updated_at", "deleted_at", "name", "protocol", "host", "port",
			"username", "password", "status", "expires_at", "fallback_mode", "backup_proxy_id", "expiry_warn_days",
		}).AddRow(
			id, now, now, nil, "proxy", "http", host, 8080,
			username, password, billing.StatusActive, nil, egress.FallbackModeNone, nil, 0,
		))
}

func TestEnqueueProxyProviderChangesChunksLargePayloads(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	providerIDs := make([]int64, 1001)
	for i := range providerIDs {
		providerIDs[i] = int64(i + 1)
	}
	for start := 0; start < len(providerIDs); start += 500 {
		end := min(start+500, len(providerIDs))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)")).
			WithArgs(scheduler.SchedulerOutboxEventProviderBulkChanged, nil, nil, proxyProviderIDsPayloadMatcher{want: providerIDs[start:end]}).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}

	err = newProxyStoreContract(nil, db).EnqueueProviderChanges(context.Background(), db, providerIDs)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
