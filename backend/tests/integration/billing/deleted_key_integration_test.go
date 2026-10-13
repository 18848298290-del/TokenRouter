//go:build integration

package billing_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Already admitted usage still needs settlement when its credential row has
// been soft deleted. The PostgreSQL matrix checks balance and subscription
// funding with quota-only, rolling-only, or combined key costs, while deletion
// happens before, after, or concurrently with the initial settlement. A missing
// key is a separate failure case whose balance and deduplication claim remain
// unchanged.
//
// Concurrent deletion uses a buffered result channel and is joined before
// settlement replay and raw-row assertions. The test retains the tombstoned
// credential and deletion timestamp while inspecting selected counters and the
// payer's funding source. Reapplying the same request reports unapplied, so
// final amounts also expose a duplicated charge in the exercised deletion
// schedules.

// TestSettlementDeletedKey settles admitted usage around soft deletion across
// funding and key-cost combinations. Replay changes no amounts, tombstones
// remain, and a missing key rolls back balance and deduplication.
func TestSettlementDeletedKey(t *testing.T) {
	db := integrationDB
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, mode := range []string{billing.APIKeyBillingModeBalance, billing.APIKeyBillingModeSubscription} {
		for _, limit := range []string{"quota", "rolling", "both"} {
			for _, order := range []string{"before", "after", "concurrent"} {
				t.Run(fmt.Sprintf("%s/%s/%s", mode, limit, order), func(t *testing.T) {
					var userID, keyID, subscriptionID int64
					require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'test',10) RETURNING id`, t.Name()+"@test.local").Scan(&userID))
					require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,quota,rate_limit_5h,rate_limit_1d,rate_limit_7d) VALUES($1,$2,'test',1,10,10,10) RETURNING id`, userID, fmt.Sprintf("test-%d", userID)).Scan(&keyID))
					cmd := &billing.UsageBillingCommand{RequestID: t.Name(), APIKeyID: keyID, UserID: userID, APIKeyBillingMode: mode, BillableAmountUSD: 1.25}
					if limit != "rolling" {
						cmd.APIKeyQuotaCost = 1.25
					}
					if limit != "quota" {
						cmd.APIKeyRateLimitCost = 1.25
					}
					if mode == billing.APIKeyBillingModeSubscription {
						var planID int64
						require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO subscription_plans(name,price) VALUES('test',1) RETURNING id`).Scan(&planID))
						require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO user_subscriptions(user_id,plan_id,starts_at,expires_at,daily_limit_usd) VALUES($1,$2,NOW()-INTERVAL '1 hour',NOW()+INTERVAL '7 days',10) RETURNING id`, userID, planID).Scan(&subscriptionID))
						cmd.PreferredSubscriptionID = &subscriptionID
					}
					remove := func() error {
						_, err := db.ExecContext(ctx, `UPDATE api_keys SET key=$2,deleted_at=NOW() WHERE id=$1`, keyID, fmt.Sprintf("__deleted__%d", keyID))
						return err
					}
					if order == "before" {
						require.NoError(t, remove())
					}
					// Launch deletion only for the
					// overlapping schedule and keep its
					// result buffered; the test joins it
					// before replay and inspecting the
					// tombstoned row.
					var deletedDone chan error
					if order == "concurrent" {
						deletedDone = make(chan error, 1)
						go func() { deletedDone <- remove() }()
					}
					store := newSettlementFixture(db)
					result, err := store.Apply(ctx, cmd)
					require.NoError(t, err)
					require.True(t, result.Applied)
					if order == "after" {
						require.NoError(t, remove())
					}
					if deletedDone != nil {
						require.NoError(t, <-deletedDone)
					}
					// Reuse the settlement identity after
					// deletion has completed so final
					// funding and key counters reveal any
					// repeated charge from this retry.
					repeated, err := store.Apply(ctx, cmd)
					require.NoError(t, err)
					require.False(t, repeated.Applied)
					var balance, quota, rolling float64
					var credential, status string
					var deleted sql.NullTime
					require.NoError(t, db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
					require.NoError(t, db.QueryRowContext(ctx, `SELECT quota_used,usage_5h,key,status,deleted_at FROM api_keys WHERE id=$1`, keyID).Scan(&quota, &rolling, &credential, &status, &deleted))
					require.True(t, deleted.Valid)
					require.Equal(t, fmt.Sprintf("__deleted__%d", keyID), credential)
					if order == "before" {
						require.Equal(t, "active", status)
					}
					if limit != "rolling" {
						require.Equal(t, 1.25, quota)
					} else {
						require.Zero(t, quota)
					}
					if limit != "quota" {
						require.Equal(t, 1.25, rolling)
					} else {
						require.Zero(t, rolling)
					}
					if mode == billing.APIKeyBillingModeBalance {
						require.Equal(t, 8.75, balance)
					} else {
						require.Equal(t, 10.0, balance)
						var used float64
						require.NoError(t, db.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, subscriptionID).Scan(&used))
						require.Equal(t, 1.25, used)
					}
				})
			}
		}
	}
	// A missing key makes settlement fail after its funding work begins;
	// inspect both payer balance and the request claim to detect partially
	// committed effects.
	t.Run("missing_key_rolls_back", func(t *testing.T) {
		var userID int64
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES('missing-key@test.local','test',10) RETURNING id`).Scan(&userID))
		cmd := &billing.UsageBillingCommand{RequestID: t.Name(), APIKeyID: 9223372036854775807, UserID: userID, APIKeyBillingMode: billing.APIKeyBillingModeBalance, BillableAmountUSD: 1.25, APIKeyQuotaCost: 1.25}
		_, err := newSettlementFixture(db).Apply(ctx, cmd)
		require.Error(t, err)
		var balance float64
		require.NoError(t, db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
		require.Equal(t, 10.0, balance)
		var records int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, cmd.RequestID).Scan(&records))
		require.Zero(t, records)
	})
}
