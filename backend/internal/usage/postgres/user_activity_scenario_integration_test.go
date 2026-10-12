//go:build integration

package postgres

// These scenarios compare usage writes and reads with the activity-summary triggers
// in migrations/289_usage_user_activity.sql.

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

// TestUserActivityTransactions compares summaries with raw logs after deletion, reassignment, deduplication, and rollback.
func TestUserActivityTransactions(t *testing.T) {
	client := testEntClient(t)
	ctx := context.Background()
	u := mustCreateUser(t, client, &identity.User{Email: "activity@test.local"})
	v := mustCreateUser(t, client, &identity.User{Email: "activity-other@test.local"})
	k := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: "activity-key"})
	p := mustCreateProvider(t, client, &provider.Record{Name: "activity"})
	insert := `INSERT INTO usage_logs(user_id,api_key_id,provider_id,model,request_id,created_at)
		VALUES($1,$2,$3,'test',$4,$5) ON CONFLICT(request_id,api_key_id) DO NOTHING`
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
	require.NoError(t, tx.Rollback())
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id=$1 AND created_at=$2", u.ID, latest)
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_logs SET user_id=$1,created_at=$2 WHERE user_id=$3", v.ID, latest, u.ID)
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
	// Repeated migration backfills must still agree with existing detail records.
	migration, err := migrations.FS.ReadFile("289_usage_user_activity.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = integrationDB.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		assertActivityMatchesLogs(t, integrationDB)
	}
	_, err = integrationDB.ExecContext(ctx, "TRUNCATE usage_logs CASCADE")
	require.NoError(t, err)
	assertActivityMatchesLogs(t, integrationDB)
}

// TestUserActivityConcurrentWrites interleaves batch inserts and cleanup for one user across connections.
func TestUserActivityConcurrentWrites(t *testing.T) {
	client := testEntClient(t)
	u := mustCreateUser(t, client, &identity.User{Email: "activity-concurrent@test.local"})
	k := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: "activity-concurrent"})
	p := mustCreateProvider(t, client, &provider.Record{Name: "activity-concurrent"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	errors := make(chan error, 8)
	var workers sync.WaitGroup
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
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	assertActivityMatchesLogs(t, integrationDB)
}

// assertActivityMatchesLogs checks all users for missing and extra summary rows.
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
