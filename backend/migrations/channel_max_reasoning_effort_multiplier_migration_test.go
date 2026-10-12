package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelMaxReasoningEffortMultiplierMigration inspects the maximum-reasoning
// multiplier SQL. It requires the decimal column and named constraint allowing
// NULL or positive values, protecting the migration's optional multiplier rule.
func TestChannelMaxReasoningEffortMultiplierMigration(t *testing.T) {
	content, err := FS.ReadFile("268_channel_max_reasoning_effort_multiplier.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "max_reasoning_effort_multiplier NUMERIC(20,8)")
	require.Contains(t, sql, "channel_model_pricing_max_reasoning_effort_multiplier_positive")
	require.Contains(t, sql, "max_reasoning_effort_multiplier IS NULL OR max_reasoning_effort_multiplier > 0")
}
