package usageview

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateNormalizedUsageRejectsUnknownMode supplies an unsupported mode
// and a balance mode lacking its required amount. It checks that both calls
// return a validation error for those fixtures.
func TestValidateNormalizedUsageRejectsUnknownMode(t *testing.T) {
	err := ValidateNormalizedUsage(&UpstreamUsageInfo{Provider: "test", Mode: "window"})
	require.Error(t, err)
	err = ValidateNormalizedUsage(&UpstreamUsageInfo{Provider: "test", Mode: "balance"})
	require.Error(t, err)
}
