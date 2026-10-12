//go:build integration

// These tests cover quota-window increments through the key usage store and settlement store.

package billing_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/postgres"
)

func TestKeyRateLimitWindowWrites(t *testing.T) {
	for _, writer := range []string{"key_store", "settlement"} {
		for _, state := range []string{"empty", "expired", "active"} {
			t.Run(writer+"/"+state, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				db := integrationDB
				var before time.Time
				require.NoError(t, db.QueryRowContext(ctx, `SELECT NOW()`).Scan(&before))
				var start sql.NullTime
				used, want := 0.0, 1.25
				switch state {
				case "expired":
					start = sql.NullTime{Time: before.Add(-8 * 24 * time.Hour), Valid: true}
					used = 3
				case "active":
					start = sql.NullTime{Time: before.Add(-time.Hour), Valid: true}
					used, want = 3, 4.25
				}
				var userID, keyID int64
				require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'fixture',10) RETURNING id`, t.Name()+"@test.local").Scan(&userID))
				require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys
					(user_id,key,name,usage_5h,usage_1d,usage_7d,window_5h_start,window_1d_start,window_7d_start)
					VALUES($1,$2,'fixture',$3,$3,$3,$4,$4,$4) RETURNING id`, userID, t.Name(), used, start).Scan(&keyID))
				if writer == "key_store" {
					require.NoError(t, postgres.NewKeyUsageStore(nil, db).IncrementRateLimitUsage(ctx, keyID, 1.25))
				} else {
					result, err := newSettlementFixture(db).Apply(ctx, &billing.UsageBillingCommand{
						RequestID: t.Name(), APIKeyID: keyID, UserID: userID,
						APIKeyBillingMode: billing.APIKeyBillingModeBalance,
						BillableAmountUSD: 1.25, APIKeyRateLimitCost: 1.25,
					})
					require.NoError(t, err)
					require.True(t, result.Applied)
				}
				var after time.Time
				require.NoError(t, db.QueryRowContext(ctx, `SELECT NOW()`).Scan(&after))
				var usage5h, usage1d, usage7d float64
				var start5h, start1d, start7d time.Time
				var dailyAtMidnight, weeklyAtMidnight bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT usage_5h,usage_1d,usage_7d,
					window_5h_start,window_1d_start,window_7d_start,
					window_1d_start = date_trunc('day',window_5h_start),
					window_7d_start = date_trunc('day',window_5h_start)
					FROM api_keys WHERE id=$1`, keyID).Scan(&usage5h, &usage1d, &usage7d, &start5h, &start1d, &start7d, &dailyAtMidnight, &weeklyAtMidnight))
				require.Equal(t, want, usage5h)
				require.Equal(t, want, usage1d)
				require.Equal(t, want, usage7d)
				if state == "active" {
					for _, got := range []time.Time{start5h, start1d, start7d} {
						require.True(t, start.Time.Equal(got))
					}
				} else {
					require.False(t, start5h.Before(before))
					require.False(t, start5h.After(after))
					require.True(t, dailyAtMidnight)
					require.True(t, weeklyAtMidnight)
				}
			})
		}
	}
}
