package provider_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Expiry maintenance reports the provider IDs changed by its update statement.
// SQL mocks use those returned IDs to check the bulk scheduling payload and
// verify that an empty update result produces zero changes and no additional
// outbox write.

// providerIDsPayloadMatcher compares ordered provider IDs returned by expiry
// maintenance with event arguments.
type providerIDsPayloadMatcher struct {
	// want retains the expected slice until sqlmock evaluates the argument;
	// callers keep its values and order stable through that comparison.
	want []int64 // expected ordered provider IDs
}

// Match accepts byte-slice JSON with a provider-ID slice deeply equal to want.
// Slice order and nil versus empty must match; other value types or decoding
// errors reject the SQL mock argument.
func (m providerIDsPayloadMatcher) Match(value driver.Value) bool {
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

// TestAutoPauseExpiredProvidersEnqueuesAffectedProviders returns two changed
// IDs from the mocked expiry update. The repository reports two changes and
// enqueues those IDs in the expected bulk-event order.
func TestAutoPauseExpiredProvidersEnqueuesAffectedProviders(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectQuery(`(?s)UPDATE providers.*RETURNING id`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)).AddRow(int64(29)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)")).
		WithArgs(scheduler.SchedulerOutboxEventProviderBulkChanged, nil, nil, providerIDsPayloadMatcher{want: []int64{11, 29}}).
		WillReturnResult(sqlmock.NewResult(1, 1))

	repo := newProviderStoreContract(nil, db, nil)
	updated, err := repo.AutoPauseExpiredProviders(context.Background(), now)

	require.NoError(t, err)
	require.EqualValues(t, 2, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAutoPauseExpiredProvidersSkipsOutboxWithoutChanges returns an empty ID
// set from the mocked expiry update. The repository reports zero changes and
// completes without an additional event write.
func TestAutoPauseExpiredProvidersSkipsOutboxWithoutChanges(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	mock.ExpectQuery(`(?s)UPDATE providers.*RETURNING id`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	repo := newProviderStoreContract(nil, db, nil)
	updated, err := repo.AutoPauseExpiredProviders(context.Background(), now)

	require.NoError(t, err)
	require.Zero(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}
