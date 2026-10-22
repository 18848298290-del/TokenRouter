package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations_test.go checks delivery declarations for generated promotion codes.
// Code-bearing results need a protected file reference so creation and subsequent
// lookup can expose the original response through the administrator's browser workflow.

// TestPromoCodeOperationsDeliverGeneratedValues checks all operations that return complete promotion records.
// Their common file mode preserves generated code values for the administrator download, including after later lookup or update.
func TestPromoCodeOperationsDeliverGeneratedValues(t *testing.T) {
	for _, id := range []string{"promo_codes.create", "promo_codes.get", "promo_codes.list", "promo_codes.update"} {
		require.Equal(t, adminapi.ResponseFile, ManagementOperations[id].Response, id)
	}
}
