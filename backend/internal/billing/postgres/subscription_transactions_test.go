package postgres

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
)

// lockOrderGroupReader detects an unintended group lookup by panicking after
// the lock probe.
type lockOrderGroupReader struct{}

// TestAssignOrExtendSubscriptionSerializesWithUserRowLock makes the mocked
// user FOR UPDATE query fail inside an owned grant transaction. It checks that
// error propagates and rollback occurs before unexpected group/chain work; it
// exercises one failing call rather than a concurrent grant schedule.
func TestAssignOrExtendSubscriptionSerializesWithUserRowLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the SQL mock handle at test cleanup. Close errors are discarded
	// here while explicit mock expectations separately determine the observed
	// lock/rollback result.
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	// Close the Ent wrapper during cleanup as well; these resource-release errors
	// are discarded, and the test outcome comes from the service error and query
	// expectations.
	t.Cleanup(func() { _ = client.Close() })

	lockProbeErr := errors.New("stop after user row lock")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "users".*FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnError(lockProbeErr)
	mock.ExpectRollback()

	svc := billing.NewSubscriptionService(lockOrderGroupReader{}, billingtestkit.SubscriptionRepositoryNoop{}, NewSubscriptionMutations(client))
	_, _, err = svc.AssignOrExtendSubscription(context.Background(), &billing.AssignSubscriptionInput{
		UserID:              42,
		PlanID:              7,
		ValidityDays:        30,
		UseProvidedTemplate: true,
	})

	require.ErrorIs(t, err, lockProbeErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GetByIDLite panics on every attempted fixture group lookup with the
// unexpected-call message. The lock-failure scenario relies on stopping before
// this method, so reaching it exposes later service work after the probe
// error.
func (lockOrderGroupReader) GetByIDLite(context.Context, int64) (*billing.SubscriptionPlanGroup, error) {
	panic("unexpected GetByIDLite call")
}
