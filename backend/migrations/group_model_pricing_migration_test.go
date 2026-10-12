package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration246AddsGroupModelPricingWithLongContextEnabled inspects group pricing
// schema and backfill SQL. It requires JSONB pricing, a true long-context
// default, and an update covering existing values distinct from true.
func TestMigration246AddsGroupModelPricingWithLongContextEnabled(t *testing.T) {
	content, err := FS.ReadFile("246_group_model_pricing.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "long_context_pricing_enabled BOOLEAN NOT NULL DEFAULT TRUE")
	require.Contains(t, sql, "model_pricing JSONB")
	require.Contains(t, sql, "SET long_context_pricing_enabled = TRUE")
	require.Contains(t, sql, "WHERE long_context_pricing_enabled IS DISTINCT FROM TRUE")
}
