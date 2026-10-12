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

// lockOrderGroupReader panics on group lookup to detect work performed after the user lock fails.
type lockOrderGroupReader struct{}

// TestAssignOrExtendSubscriptionSerializesWithUserRowLock checks that grants lock the user before reading the
// subscription chain.
func TestAssignOrExtendSubscriptionSerializesWithUserRowLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
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

func (lockOrderGroupReader) GetByIDLite(context.Context, int64) (*billing.SubscriptionPlanGroup, error) {
	panic("unexpected GetByIDLite call")
}
