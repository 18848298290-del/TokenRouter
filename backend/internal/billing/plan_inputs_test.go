package billing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdatePlanRequest_UnmarshalNullablePatchFields decodes into fresh
// requests with omitted, null, zero, and positive nullable fields.
// It checks omitted presence bits and the received null, zero, and positive
// pointer values, keeping clearing distinguishable from omission and a numeric
// zero replacement.
func TestUpdatePlanRequest_UnmarshalNullablePatchFields(t *testing.T) {
	var omitted UpdatePlanRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"Basic"}`), &omitted))
	require.False(t, omitted.OriginalPrice.Present)
	require.False(t, omitted.DailyLimitUSD.Present)
	require.False(t, omitted.WeeklyLimitUSD.Present)
	require.False(t, omitted.MonthlyLimitUSD.Present)

	var patched UpdatePlanRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"original_price": null,
		"daily_limit_usd": null,
		"weekly_limit_usd": 0,
		"monthly_limit_usd": 12.5
	}`), &patched))
	require.True(t, patched.OriginalPrice.Present)
	require.Nil(t, patched.OriginalPrice.Value)
	require.True(t, patched.DailyLimitUSD.Present)
	require.Nil(t, patched.DailyLimitUSD.Value)
	require.True(t, patched.WeeklyLimitUSD.Present)
	require.NotNil(t, patched.WeeklyLimitUSD.Value)
	require.Equal(t, 0.0, *patched.WeeklyLimitUSD.Value)
	require.True(t, patched.MonthlyLimitUSD.Present)
	require.NotNil(t, patched.MonthlyLimitUSD.Value)
	require.Equal(t, 12.5, *patched.MonthlyLimitUSD.Value)
}
