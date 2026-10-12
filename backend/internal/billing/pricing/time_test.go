package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExplicitTimeLocationPreservesDSTRepeatedHour checks both repeated 1 a.m. hours and the exclusive window end.
func TestExplicitTimeLocationPreservesDSTRepeatedHour(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	config := &TimePricingConfig{Timezone: "America/New_York", Periods: []TimePricingPeriod{{StartTime: "01:00", EndTime: "02:00", Multiplier: 2}}}
	for _, hour := range []int{5, 6} {
		require.Equal(t, 2.0, config.MultiplierAt(time.Date(2026, 11, 1, hour, 30, 0, 0, time.UTC), location))
	}
	require.Equal(t, 1.0, config.MultiplierAt(time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), location))
}
