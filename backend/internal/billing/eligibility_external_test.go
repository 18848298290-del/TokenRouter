package billing_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// TestCheckEffectiveSubscriptionEligibility_DailyTailUsesFiniteMonthlyLimit
// supplies an exhausted old daily window, a two-hour lifetime tail and
// remaining monthly quota. The effective hot-path check must accept the grant
// after locally treating the eligible daily period as reset.
func TestCheckEffectiveSubscriptionEligibility_DailyTailUsesFiniteMonthlyLimit(t *testing.T) {
	now := time.Now().UTC()
	dailyWindowStart := now.Add(-25 * time.Hour)
	dailyLimit := 200.0
	monthlyLimit := 1000.0
	sub := &billing.UserSubscription{
		Status:           billing.SubscriptionStatusActive,
		StartsAt:         now.AddDate(0, 0, -30),
		ExpiresAt:        now.Add(2 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyLimitUSD:    &dailyLimit,
		MonthlyLimitUSD:  &monthlyLimit,
		DailyUsageUSD:    dailyLimit,
		MonthlyUsageUSD:  860.98,
	}

	require.NoError(t, billing.CheckEffectiveSubscriptionEligibility(sub, timezone.NewCalendar(time.UTC)), "有限月额度未耗尽时，尾段日窗口应通过计费热路径预检")
}
