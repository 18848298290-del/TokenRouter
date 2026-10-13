package zhipu

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A weekly reset can appear before a five-hour reset in the same quota
// response. The fixtures check that explicit units retain window identity,
// while missing-unit observations use the vacant-slot heuristic and unrelated
// allowance types produce no token window.

// TestParseZhipuTokenTiers_UnitClassification places the weekly reset before
// the five-hour reset. It checks that explicit units still select the correct
// window order and percentage values.
func TestParseZhipuTokenTiers_UnitClassification(t *testing.T) {
	t.Parallel()
	// Reverse chronological order in the fixture so reset sorting would
	// assign the wrong windows if it overrode the explicit unit
	// identifiers.
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","unit":6,"percentage":70,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":20,"nextResetTime":1700000099999}
		]
	}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	// Use an absolute tolerance for the decoded percentage comparison.
	require.InDelta(t, 20.0, tiers[0].UsedPercent, 1e-9)
	require.Equal(t, "7d", tiers[1].Window)
	// Compare the weekly percentage with the same absolute tolerance.
	require.InDelta(t, 70.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_SingleTierOldPlan supplies one explicitly identified
// five-hour token entry. It checks that the parser returns exactly one
// observation in that slot, so a weekly entry is optional.
func TestParseZhipuTokenTiers_SingleTierOldPlan(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":15,"nextResetTime":1700000000000}]}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 1)
	require.Equal(t, "5h", tiers[0].Window)
}

// TestParseZhipuTokenTiers_FallbackHeuristic omits both units and supplies a
// reset for just one entry. It checks that the missing-reset entry fills the
// five-hour slot and the remaining entry fills weekly.
func TestParseZhipuTokenTiers_FallbackHeuristic(t *testing.T) {
	t.Parallel()
	// Omit both unit identities and one reset so classification has to
	// exercise missing-reset precedence while filling the two initially
	// vacant slots.
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","percentage":50,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","percentage":10}
		]
	}`)
	tiers := ParseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	// Check the short-window percentage with the fixture's floating-point
	// tolerance.
	require.InDelta(t, 10.0, tiers[0].UsedPercent, 1e-9)
	require.Equal(t, "7d", tiers[1].Window)
	// Apply the same absolute tolerance to the weekly observation.
	require.InDelta(t, 50.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_IgnoresNonTokenEntries supplies an OTHER_LIMIT entry
// with a recognized unit. It checks that an unrelated allowance type cannot
// create a percentage observation despite that unit value.
func TestParseZhipuTokenTiers_IgnoresNonTokenEntries(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"OTHER_LIMIT","unit":3,"percentage":99}]}`)
	require.Empty(t, ParseZhipuTokenTiers(data))
}
