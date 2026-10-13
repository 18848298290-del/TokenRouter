package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestBatchUpsertIngressRejectsUsesFixedMultiRowChunks supplies one more row
// than the fixed input-segment limit. It checks two INSERT attempts occur
// between one BEGIN and COMMIT using SQL expectations, without validating live
// conflict behavior.
func TestBatchUpsertIngressRejectsUsesFixedMultiRowChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock database after the test, keeping its resource lifetime
	// separate from the transaction expectations being asserted.
	t.Cleanup(func() { _ = db.Close() })
	repo := &Store{db: db}
	now := time.Now().UTC().Truncate(time.Minute)
	// Cross the fixed segment limit by one fixture row.
	items := make([]*ops.OpsIngressRejectAggregate, ingressRejectUpsertChunkSize+1)
	for i := range items {
		items[i] = &ops.OpsIngressRejectAggregate{
			BucketStart: now, RejectReason: "invalid_api_key", RouteFamily: "messages",
			Protocol: "anthropic", ClientIP: "192.0.2.1", RequestCount: 1, FirstSeen: now, LastSeen: now,
		}
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO ops_ingress_reject_aggregates").WillReturnResult(sqlmock.NewResult(0, int64(ingressRejectUpsertChunkSize)))
	mock.ExpectExec("INSERT INTO ops_ingress_reject_aggregates").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.BatchUpsertIngressRejects(context.Background(), items))
	require.NoError(t, mock.ExpectationsWereMet())
}
