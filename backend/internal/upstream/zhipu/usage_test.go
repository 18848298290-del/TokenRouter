package zhipu

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestParseZhipuTokenTiers_UnitClassification checks that unit identifies each window.
// The weekly window can reset before the five-hour window near the end of a period.
func TestParseZhipuTokenTiers_UnitClassification(t *testing.T) {
	t.Parallel()
	// Place the weekly reset before the five-hour reset.
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","unit":6,"percentage":70,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":20,"nextResetTime":1700000099999}
		]
	}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	require.InDelta(t, 20.0, tiers[0].UsedPercent, 1e-9)
	require.Equal(t, "7d", tiers[1].Window)
	require.InDelta(t, 70.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_SingleTierOldPlan checks a single five-hour quota entry.
func TestParseZhipuTokenTiers_SingleTierOldPlan(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":15,"nextResetTime":1700000000000}]}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 1)
	require.Equal(t, "5h", tiers[0].Window)
}

// TestParseZhipuTokenTiers_FallbackHeuristic checks missing-unit classification:
// entries without resets fill the five-hour slot before entries with resets.
func TestParseZhipuTokenTiers_FallbackHeuristic(t *testing.T) {
	t.Parallel()
	// Neither entry has a unit; the entry without a reset fills the five-hour slot.
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","percentage":50,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","percentage":10}
		]
	}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	require.InDelta(t, 10.0, tiers[0].UsedPercent, 1e-9) // Missing reset takes the five-hour slot.
	require.Equal(t, "7d", tiers[1].Window)
	require.InDelta(t, 50.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_IgnoresNonTokenEntries excludes unrelated allowance types.
func TestParseZhipuTokenTiers_IgnoresNonTokenEntries(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"OTHER_LIMIT","unit":3,"percentage":99}]}`)
	require.Empty(t, ParseZhipuTokenTiers(data))
}
