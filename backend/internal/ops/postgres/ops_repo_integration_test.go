//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestOpsRequestIDConditionUsesIndexes builds analyzed temporary log tables
// with twenty thousand rows and a client/local-ID collision. It protects
// exact-local precedence, client-ID lookup, missing results, and absence of
// sequential scans for the tested fixture plans in both log tables.
func TestOpsRequestIDConditionUsesIndexes(t *testing.T) {
	ctx := t.Context()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	// Keep the temporary tables and fixture writes inside one transaction.
	// Deferred rollback removes them after assertions, suppressing cleanup errors
	// while retaining test failures.
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
CREATE TEMP TABLE usage_logs (LIKE public.usage_logs INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE request_records (LIKE public.request_records INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE ops_error_logs (LIKE public.ops_error_logs INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE ops_system_logs (LIKE public.ops_system_logs INCLUDING ALL) ON COMMIT DROP;
INSERT INTO ops_error_logs(request_id,client_request_id,error_phase,error_type,status_code)
SELECT 'request-'||g,'client-'||g,'request','api_error',500 FROM generate_series(1,20000) g;
INSERT INTO ops_system_logs(request_id,client_request_id,level,message)
SELECT 'request-'||g,'client-'||g,'error','fixture' FROM generate_series(1,20000) g;
UPDATE ops_error_logs SET client_request_id='request-1' WHERE request_id='request-2';
UPDATE ops_system_logs SET client_request_id='request-1' WHERE request_id='request-2';
INSERT INTO request_records(request_id,started_at,updated_at,revision,record)
VALUES('request-1',now(),now(),1,'{"request_id":"request-1","state":"failed"}');
ANALYZE usage_logs;
ANALYZE request_records;
ANALYZE ops_error_logs;
ANALYZE ops_system_logs;`)
	require.NoError(t, err)
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		for _, test := range []struct {
			search string // requested correlation lookup text
			want   string // expected stored request identity
		}{
			{"request-1", "request-1"},
			{"client-3", "request-3"},
			{"missing", ""},
		} {
			t.Run(table+"/"+test.search, func(t *testing.T) {
				query := fmt.Sprintf("SELECT l.request_id FROM %s l WHERE l.id>$1 AND %s", table, opsRequestIDCondition("l", 2))
				rows, err := tx.QueryContext(ctx, query, 0, test.search)
				require.NoError(t, err)
				var got []string
				for rows.Next() {
					var id string
					require.NoError(t, rows.Scan(&id))
					got = append(got, id)
				}
				require.NoError(t, rows.Err())
				// Close each query cursor before the next EXPLAIN on the same transaction,
				// so result resources do not remain active during plan inspection.
				require.NoError(t, rows.Close())
				if test.want == "" {
					require.Empty(t, got)
				} else {
					require.Equal(t, []string{test.want}, got)
				}
				var raw []byte
				require.NoError(t, tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, 0, test.search).Scan(&raw))
				// planNode retains the plan operation and relation needed to inspect nested
				// PostgreSQL scan nodes.
				type planNode struct {
					Type     string     `json:"Node Type"`     // PostgreSQL plan operation name
					Relation string     `json:"Relation Name"` // scanned PostgreSQL relation name
					Plans    []planNode `json:"Plans"`         // nested child plan operations
				}
				var plans []struct {
					Plan planNode `json:"Plan"` // root PostgreSQL plan node
				}
				// Decode EXPLAIN's nested JSON tree and inspect matching log-table nodes,
				// so the assertion tests their chosen scan operations rather than another
				// relation's plan.
				require.NoError(t, json.Unmarshal(raw, &plans))
				seen := false
				var inspect func(planNode)
				inspect = func(node planNode) {
					if node.Relation == table {
						seen = true
						require.NotEqual(t, "Seq Scan", node.Type, "%s", raw)
					}
					for _, child := range node.Plans {
						inspect(child)
					}
				}
				inspect(plans[0].Plan)
				require.True(t, seen)
			})
		}
	}
}

// TestGetErrorLogByID_APIKeyPrefixAndUpstreamStatus stores missing and present
// saved key prefixes plus a credential failure with upstream zero. It protects
// returned prefix snapshots and a nonnil zero status through real PostgreSQL
// reads.
func TestGetErrorLogByID_APIKeyPrefixAndUpstreamStatus(t *testing.T) {
	ctx := context.Background()
	// Clear the shared error fixture before these round trips. Setup truncation
	// errors are suppressed, while the subsequent required inserts and reads
	// determine this test.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	repo := NewOpsRepository(integrationDB)
	var plainID int64
	err := integrationDB.QueryRowContext(ctx, `
		INSERT INTO ops_error_logs (
			error_phase, error_type, severity, status_code, created_at
		) VALUES (
			'upstream', 'upstream_error', 'error', 500, NOW()
		) RETURNING id`,
	).Scan(&plainID)
	require.NoError(t, err)

	plain, err := repo.GetErrorLogByID(ctx, plainID)
	require.NoError(t, err)
	require.Empty(t, plain.APIKeyPrefix)
	validID, err := repo.InsertErrorLog(ctx, &ops.OpsInsertErrorLogInput{
		ErrorPhase:   "request",
		ErrorType:    "api_error",
		Severity:     "error",
		StatusCode:   402,
		CreatedAt:    time.Now(),
		APIKeyPrefix: "sk-valid",
	})
	require.NoError(t, err)

	valid, err := repo.GetErrorLogByID(ctx, validID)
	require.NoError(t, err)
	require.Equal(t, "sk-valid", valid.APIKeyPrefix)
	zero := 0
	credentialFailureID, err := repo.InsertErrorLog(ctx, &ops.OpsInsertErrorLogInput{
		ErrorPhase:         "provider_auth",
		ErrorType:          "upstream_error",
		Severity:           "error",
		StatusCode:         503,
		UpstreamStatusCode: &zero,
		CreatedAt:          time.Now(),
	})
	require.NoError(t, err)

	credentialFailure, err := repo.GetErrorLogByID(ctx, credentialFailureID)
	require.NoError(t, err)
	require.NotNil(t, credentialFailure.UpstreamStatusCode)
	require.Zero(t, *credentialFailure.UpstreamStatusCode)
}

// TestOpsRepositoryBatchInsertErrorLogs inserts two prepared observations with
// distinct request IDs and phases. It protects the successful transactional
// batch count and two stored matching rows in PostgreSQL.
func TestOpsRepositoryBatchInsertErrorLogs(t *testing.T) {
	ctx := context.Background()
	// Reset shared error rows before the batch fixture. Setup truncation errors
	// are suppressed; assertions count the two distinct request IDs submitted
	// here.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY")

	repo := NewOpsRepository(integrationDB)
	now := time.Now().UTC()
	inserted, err := repo.BatchInsertErrorLogs(ctx, []*ops.OpsInsertErrorLogInput{
		{
			RequestID:    "batch-ops-1",
			ErrorPhase:   "upstream",
			ErrorType:    "upstream_error",
			Severity:     "error",
			StatusCode:   429,
			ErrorMessage: "rate limited",
			CreatedAt:    now,
		},
		{
			RequestID:    "batch-ops-2",
			ErrorPhase:   "internal",
			ErrorType:    "api_error",
			Severity:     "error",
			StatusCode:   500,
			ErrorMessage: "internal error",
			CreatedAt:    now.Add(time.Millisecond),
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, inserted)

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_error_logs WHERE request_id IN ('batch-ops-1', 'batch-ops-2')").Scan(&count))
	require.Equal(t, 2, count)
}
