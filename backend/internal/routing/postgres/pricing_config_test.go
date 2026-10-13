package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Pricing configuration lists accept an explicit descending ID order. The
// selected SQL fragment includes both the main column and its ID tie column, so
// the assertion checks the complete fragment returned to the query builder.

// TestPricingConfigListOrderBy_AllowsDescendingIDSort supplies ID sorting with
// a descending direction. It checks the complete generated order clause so list
// queries honor the selected direction.
func TestPricingConfigListOrderBy_AllowsDescendingIDSort(t *testing.T) {
	params := pagination.PaginationParams{
		SortBy:    "id",
		SortOrder: "desc",
	}

	require.Equal(t, "c.id DESC, c.id DESC", pricingConfigListOrderBy(params))
}
