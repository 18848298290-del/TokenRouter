//go:build integration

package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Provider usage accounting can participate in a caller's Ent transaction
// alongside configuration updates. PostgreSQL assertions inspect pending
// counters and cooldown changes, verify that the outer transaction controls
// visibility and outbox behavior, and read the committed row again after
// rollback.

// TestProviderUsageParticipantKeepsOuterTransaction increments and resets usage
// inside the caller's Ent transaction alongside a rename. Pending changes
// remain invisible outside, write no event, and rollback restores the committed
// counters and cooldown.
func TestProviderUsageParticipantKeepsOuterTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	// Daily and weekly quota starts use RFC3339 without fractional seconds;
	// whole-second SQL timestamps keep those encoded starts at the same fixture instant.
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(time.Hour)
	row, err := client.Provider.Create().SetName(fmt.Sprintf("test-usage-%d", time.Now().UnixNano())).
		SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeAPIKey).
		SetStatus(provider.StatusError).SetErrorMessage("preserve-health").SetSchedulable(false).
		SetRateLimitedAt(now).SetRateLimitResetAt(until).SetOverloadUntil(until).
		SetExtra(map[string]any{"quota_used": 5, "quota_limit": 6, "quota_daily_used": 2, "quota_weekly_used": 3, "quota_monthly_limit": 100, "quota_monthly_used": 9, "quota_monthly_start": now.Format(time.RFC3339), "quota_daily_start": now.Format(time.RFC3339), "quota_weekly_start": now.Format(time.RFC3339), "custom": "preserve"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	var before int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&before))
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Bind usage to the existing Ent transaction so its counters and the
	// later rename can be inspected together before the caller rolls them back.
	participant := billingpostgres.ProviderUsageInTx(tx.Client())
	crossed, err := participant.Increment(ctx, row.ID, 2)
	require.NoError(t, err)
	require.True(t, crossed)
	inside, err := tx.Client().Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, float64(7), inside.Extra["quota_used"])
	require.Equal(t, float64(11), inside.Extra["quota_monthly_used"])
	require.NoError(t, participant.ResetAndClearRateLimitCooldown(ctx, row.ID))
	_, err = tx.Client().Provider.UpdateOneID(row.ID).SetName("pending-name").Save(ctx)
	require.NoError(t, err)
	inside, err = tx.Client().Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	for _, key := range []string{"quota_used", "quota_daily_used", "quota_weekly_used", "quota_monthly_used"} {
		require.Equal(t, float64(0), inside.Extra[key], key)
	}
	require.NotContains(t, inside.Extra, "quota_daily_start")
	require.NotContains(t, inside.Extra, "quota_weekly_start")
	require.NotContains(t, inside.Extra, "quota_monthly_start")
	require.Equal(t, "preserve", inside.Extra["custom"])
	require.Nil(t, inside.RateLimitedAt)
	require.Nil(t, inside.RateLimitResetAt)
	require.Equal(t, provider.StatusError, inside.Status)
	require.NotNil(t, inside.ErrorMessage)
	require.Equal(t, "preserve-health", *inside.ErrorMessage)
	require.False(t, inside.Schedulable)
	require.NotNil(t, inside.OverloadUntil)
	outside, err := client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, row.Name, outside.Name)
	require.Equal(t, float64(5), outside.Extra["quota_used"])
	rows, err := tx.Client().QueryContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID)
	require.NoError(t, err)
	require.True(t, rows.Next())
	var pending int
	require.NoError(t, rows.Scan(&pending))
	require.NoError(t, rows.Close())
	require.Equal(t, before, pending)
	// Complete the caller-owned rollback after checking pending counters and
	// event count; the next read uses the separate committed-data client.
	require.NoError(t, tx.Rollback())
	restored, err := client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, row.Name, restored.Name)
	require.Equal(t, float64(5), restored.Extra["quota_used"])
	require.Equal(t, float64(9), restored.Extra["quota_monthly_used"])
	require.NotNil(t, restored.RateLimitResetAt)
}

// TestMonthlyQuotaSQLResetCalendar evaluates the production monthly reset SQL
// against explicit dates in PostgreSQL. Month-end clamping, leap-year handling,
// timezone conversion, and equality all produce the next future reset timestamp.
func TestMonthlyQuotaSQLResetCalendar(t *testing.T) {
	for _, tc := range []struct {
		now  string // UTC observation timestamp
		next string // expected UTC reset
	}{
		{"2026-02-28T01:00:00Z", "2026-03-31T01:00:00Z"},
		{"2028-02-29T00:00:00Z", "2028-02-29T01:00:00Z"},
		{"2026-12-31T01:00:00Z", "2027-01-31T01:00:00Z"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			expr := strings.ReplaceAll(billingpostgres.NextMonthlyResetAtExpr, "NOW()", "$1::timestamptz")
			var next string
			err := integrationDB.QueryRowContext(context.Background(), "SELECT "+expr+" FROM (SELECT $2::jsonb AS extra) provider",
				tc.now, `{"quota_monthly_reset_mode":"fixed","quota_monthly_reset_day":31,"quota_monthly_reset_hour":9,"quota_reset_timezone":"Asia/Shanghai"}`).Scan(&next)
			require.NoError(t, err)
			require.Equal(t, tc.next, next)
		})
	}
}

// TestFixedQuotaSQLClockTransitions compares SQL accounting with Go publication
// and admission across clock changes. Exhausted daily, weekly, and monthly
// providers stay blocked until the same reset instant in both implementations.
func TestFixedQuotaSQLClockTransitions(t *testing.T) {
	for _, tc := range []struct {
		name string // clock transition scenario
		zone string // configured IANA timezone
		hour int    // configured reset hour
		now  string // UTC observation timestamp
	}{
		{"New York before overlap", "America/New_York", 1, "2026-11-01T04:30:00Z"},
		{"New York within overlap", "America/New_York", 1, "2026-11-01T05:30:00Z"},
		{"New York overlap equality", "America/New_York", 1, "2026-11-01T06:00:00Z"},
		{"New York before gap", "America/New_York", 2, "2026-03-08T06:30:00Z"},
		{"New York gap equality", "America/New_York", 2, "2026-03-08T07:00:00Z"},
		{"Sydney within overlap", "Australia/Sydney", 2, "2026-04-04T15:30:00Z"},
		{"Lord Howe before gap", "Australia/Lord_Howe", 2, "2026-10-03T15:15:00Z"},
		{"Lord Howe gap equality", "Australia/Lord_Howe", 2, "2026-10-03T15:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			location, err := time.LoadLocation(tc.zone)
			require.NoError(t, err)
			for _, period := range []struct {
				name    string                       // quota period label
				period  billing.ProviderWindowPeriod // quota calculation selector
				day     int                          // configured reset day
				next    string                       // next reset expression
				expired string                       // accounting expiry expression
			}{
				{"daily", billing.ProviderDailyWindow, 1, billingpostgres.NextDailyResetAtExpr, billingpostgres.DailyExpiredExpr},
				{"weekly", billing.ProviderWeeklyWindow, int(now.In(location).Weekday()), billingpostgres.NextWeeklyResetAtExpr, billingpostgres.WeeklyExpiredExpr},
				{"monthly", billing.ProviderMonthlyWindow, now.In(location).Day(), billingpostgres.NextMonthlyResetAtExpr, billingpostgres.MonthlyExpiredExpr},
			} {
				t.Run(period.name, func(t *testing.T) {
					prefix := "quota_" + period.name
					start := now.Add(-24 * time.Hour)
					extra := map[string]any{
						prefix + "_limit":      100,
						prefix + "_used":       100,
						prefix + "_start":      start.Format(time.RFC3339),
						prefix + "_reset_mode": "fixed",
						prefix + "_reset_day":  period.day,
						prefix + "_reset_hour": tc.hour,
						"quota_reset_timezone": tc.zone,
					}
					encoded, err := json.Marshal(extra)
					require.NoError(t, err)
					expr := strings.ReplaceAll(period.next, "NOW()", "$1::timestamptz")
					var savedReset string
					require.NoError(t, integrationDB.QueryRowContext(context.Background(),
						"SELECT "+expr+" FROM (SELECT $2::jsonb AS extra) provider",
						start, string(encoded)).Scan(&savedReset))
					extra[prefix+"_reset_at"] = savedReset
					encoded, err = json.Marshal(extra)
					require.NoError(t, err)
					var sqlNext string
					var sqlExpired bool
					expiredExpr := strings.ReplaceAll(period.expired, "NOW()", "$1::timestamptz")
					require.NoError(t, integrationDB.QueryRowContext(context.Background(),
						"SELECT "+expr+", "+expiredExpr+" FROM (SELECT $2::jsonb AS extra) provider",
						now, string(encoded)).Scan(&sqlNext, &sqlExpired))
					window := billing.FixedProviderWindow{Period: period.period, Mode: "fixed", Limit: 100, Day: period.day, Hour: tc.hour}
					next, ok := billing.NextProviderFixedReset(window, location, now)
					require.True(t, ok)
					require.Equal(t, sqlNext, next.UTC().Format(time.RFC3339))
					record := &provider.Record{Extra: extra, Now: func() time.Time { return now }, LoadLocation: time.LoadLocation}
					require.Equal(t, !sqlExpired, record.IsQuotaExceeded(), "admission must wait for accounting's reset")
				})
			}
		})
	}
}
