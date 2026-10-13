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

// Changing a proxy's connection identity invalidates Ollama usage snapshots on
// affected providers and writes a scheduling event. SQL mocks check the shared
// transaction, rollback on event failure, unchanged-identity handling, and
// chunked provider-ID payloads.

// proxyProviderIDsPayloadMatcher compares ordered provider IDs in JSON
// scheduler event arguments.
type proxyProviderIDsPayloadMatcher struct {
	// want is retained until sqlmock evaluates the payload. Shared input arrays
	// can also change these expected IDs when enqueueing normalizes them in place.
	want []int64 // expected ordered provider IDs
}

// Match accepts byte-slice JSON with a provider-ID slice deeply equal to want.
// Slice order and nil versus empty must match; other value types or decoding
// errors reject the SQL mock argument.
func (m proxyProviderIDsPayloadMatcher) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok {
		return false
	}
	var payload struct {
		ProviderIDs []int64 `json:"provider_ids"` // ordered affected-provider ID list
	}
	// Treat decoding failure as an argument mismatch so sqlmock reports the
	// payload with its expectation instead of accepting a partially decoded ID list.
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return reflect.DeepEqual(m.want, payload.ProviderIDs)
}

// TestProxyUpdateInvalidatesOllamaSnapshotAndEnqueuesOutboxAtomically mocks a
// proxy host change, affected snapshot IDs, and a scheduling payload. The
// expected sequence holds proxy mutation, invalidation, and outbox write in one
// transaction before commit.
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

// TestProxyUpdateRollsBackWhenOllamaOutboxFails injects an event-insert error
// after mocked proxy and snapshot changes. The repository returns that error
// and calls rollback according to the configured SQL sequence.
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

// TestProxyUpdateSkipsOllamaInvalidationForNonIdentityChange renames a proxy
// while its connection identity remains equal to the locked row. The mock
// sequence commits after reloading and permits no snapshot-invalidation or
// event query.
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

// expectProxyUpdateReload registers an Ent proxy reload with the supplied host
// and authentication fields. The returned row uses fixed HTTP connection values
// and current timestamps so identity comparisons can proceed after the mocked
// update.
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

// TestEnqueueProxyProviderChangesChunksLargePayloads supplies 1001 ascending
// provider IDs to enqueueing. SQL expectations require three payloads of 500,
// 500, and one ID, containing those fixture IDs in ascending order.
func TestEnqueueProxyProviderChangesChunksLargePayloads(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// Use 1001 ascending IDs so two full 500-ID payloads leave one final ID;
	// assertions compare the retained slices after enqueueing normalizes input in place.
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
