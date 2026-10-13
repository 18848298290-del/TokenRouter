//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// User activity summaries represent the latest retained created_at per acting
// user, including zero-cost usage. The transaction scenario compares the
// complete summary table with raw maxima after duplicate insertion, rollback,
// deletion, user reassignment, repeated migration application and truncation. A
// two-sided difference query catches both missing summaries and summaries with
// no matching raw history.
//
// Concurrent workers share a start signal, write separate request-ID families
// for the same user, then delete their own families. Each worker publishes at
// most one error into a buffer sized for every worker, allowing the test to
// wait for all work before draining results. The final table comparison checks
// this exercised interleaving without assuming a particular database scheduling
// order.

// TestUserActivityTransactions compares activity summaries with raw maxima
// across deduplicated inserts, rollback, deletion and reassignment. Reapplying
// the activity migration twice and truncating raw usage also leave the summary
// table equal to its raw source.
func TestUserActivityTransactions(t *testing.T) {
	client := testEntClient(t)
	ctx := context.Background()
	u := mustCreateUser(t, client, &identity.User{Email: "activity@test.local"})
	v := mustCreateUser(t, client, &identity.User{Email: "activity-other@test.local"})
	k := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: "activity-key"})
	p := mustCreateProvider(t, client, &provider.Record{Name: "activity"})
	insert := `INSERT INTO usage_logs(user_id,api_key_id,provider_id,model,request_id,created_at)
		VALUES($1,$2,$3,'test',$4,$5) ON CONFLICT(request_id,api_key_id) DO NOTHING`
	// Use whole-second fixture values for timestamp equality checks.
	old := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	latest := old.Add(time.Hour)
	for i, at := range []time.Time{latest, old, latest} {
		_, err := integrationDB.ExecContext(ctx, insert, u.ID, k.ID, p.ID, fmt.Sprint(i), at)
		require.NoError(t, err)
	}
	assertActivityMatchesLogs(t, integrationDB)
	got, err := query.GetLatestUsedAtByUserIDs(ctx, integrationDB, []int64{u.ID, v.ID, u.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, latest.Equal(*got[u.ID]))
	_, err = integrationDB.ExecContext(ctx, insert, u.ID, k.ID, p.ID, "0", latest.Add(time.Hour))
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, insert, u.ID, k.ID, p.ID, "rollback", latest.Add(time.Hour))
	require.NoError(t, err)
	// Discard the newer usage insert before later mutations and comparisons,
	// exercising rollback of the trigger-maintained summary along with its raw
	// source.
	require.NoError(t, tx.Rollback())
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id=$1 AND created_at=$2", u.ID, latest)
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_logs SET user_id=$1,created_at=$2 WHERE user_id=$3", v.ID, latest, u.ID)
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
	// Reapply the migration twice against existing detail records; comparing both
	// table directions after each run catches omitted users and stale activity
	// rows.
	migration, err := migrations.FS.ReadFile("289_usage_user_activity.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = integrationDB.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		assertActivityMatchesLogs(t, integrationDB)
	}
	// Empty the raw table after reapplying the migration so the final two-sided
	// comparison also catches summaries that survive truncation.
	_, err = integrationDB.ExecContext(ctx, "TRUNCATE usage_logs CASCADE")
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
}

// TestUserActivityConcurrentWrites starts eight workers against one user's
// usage history with distinct request-ID families. After their batch
// insert/delete loops finish without reported SQL errors, the summary table
// matches raw maxima.
func TestUserActivityConcurrentWrites(t *testing.T) {
	client := testEntClient(t)
	u := mustCreateUser(t, client, &identity.User{Email: "activity-concurrent@test.local"})
	k := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: "activity-concurrent"})
	p := mustCreateProvider(t, client, &provider.Record{Name: "activity-concurrent"})
	// Give concurrent SQL operations a shared deadline so blocked database work
	// can return an error before the test waits indefinitely for its workers.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	// Each worker exits after its first error. Reserve one buffered result per
	// worker so every sender can finish before the test drains errors.
	errors := make(chan error, 8)
	var workers sync.WaitGroup
	// Each registered worker waits on start, letting the test release all SQL
	// writers together after the wait group owns their completion.
	for worker := range 8 {
		workers.Go(func() {
			<-start
			for iteration := range 10 {
				request := fmt.Sprintf("activity-%d-%d", worker, iteration)
				_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,provider_id,model,request_id,created_at)
					SELECT $1,$2,$3,'test',$4||n::text,now()+n*interval '1 second' FROM generate_series(1,3) n`, u.ID, k.ID, p.ID, request)
				if err == nil {
					_, err = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE request_id LIKE $1 AND api_key_id=$2", request+"%", k.ID)
				}
				if err != nil {
					errors <- err
					return
				}
			}
		})
	}
	// Release the start signal after registering every worker, then wait for all
	// sends to finish before closing and draining the error channel.
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	assertActivityMatchesLogs(t, integrationDB)
}

// assertActivityMatchesLogs compares the entire activity table with per-user
// raw timestamp maxima and fails the test on differences. Both EXCEPT
// directions detect missing or extra values, and query errors fail before
// equality is asserted.
func assertActivityMatchesLogs(t *testing.T, db *sql.DB) {
	t.Helper()
	var differences int
	err := db.QueryRow(`WITH expected AS (SELECT user_id,MAX(created_at) AS last_used_at FROM usage_logs GROUP BY user_id)
		SELECT COUNT(*) FROM (
		 (SELECT * FROM expected EXCEPT SELECT * FROM usage_user_activity)
		 UNION ALL (SELECT * FROM usage_user_activity EXCEPT SELECT * FROM expected)
		) differences`).Scan(&differences)
	require.NoError(t, err)
	require.Zero(t, differences)
}
