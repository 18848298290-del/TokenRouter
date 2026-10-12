package forward

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpstreamFailoverErrorNextProviderActionPreservesLegacyRetry supplies zero,
// explicit retry, and stop actions. The zero action keeps provider-switch
// permission while explicit stop denies it, matching the legacy caller default.
func TestUpstreamFailoverErrorNextProviderActionPreservesLegacyRetry(t *testing.T) {
	t.Parallel()

	require.True(t, (&UpstreamFailoverError{}).ShouldRetryNextProvider())
	require.True(t, (&UpstreamFailoverError{NextProviderAction: NextProviderRetry}).ShouldRetryNextProvider())
	require.False(t, (&UpstreamFailoverError{NextProviderAction: NextProviderStop}).ShouldRetryNextProvider())
}
