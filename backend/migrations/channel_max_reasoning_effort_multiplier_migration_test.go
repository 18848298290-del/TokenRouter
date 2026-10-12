package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelMaxReasoningEffortMultiplierMigration checks the max reasoning multiplier column and
// positive-value constraint.
func TestChannelMaxReasoningEffortMultiplierMigration(t *testing.T) {
	content, err := FS.ReadFile("268_channel_max_reasoning_effort_multiplier.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "max_reasoning_effort_multiplier NUMERIC(20,8)")
	require.Contains(t, sql, "channel_model_pricing_max_reasoning_effort_multiplier_positive")
	require.Contains(t, sql, "max_reasoning_effort_multiplier IS NULL OR max_reasoning_effort_multiplier > 0")
}
