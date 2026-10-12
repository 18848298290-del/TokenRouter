package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLookupVideoBillingResolutionReportsUnknownTiers checks supported aliases
// and unsupported resolution strings. Known inputs return present values,
// unknown inputs stay absent, and normalization selects 480p or the recognized
// full-HD tier.
func TestLookupVideoBillingResolutionReportsUnknownTiers(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"480", "480p", "SD", "720", "hd", "1080", "full-hd", " fhd "} {
		normalized, ok := LookupVideoBillingResolution(in)
		require.True(t, ok, "input=%q", in)
		require.NotEmpty(t, normalized)
	}
	for _, in := range []string{"", "4k", "1080i", "2160p", "potato"} {
		normalized, ok := LookupVideoBillingResolution(in)
		require.False(t, ok, "input=%q", in)
		require.Empty(t, normalized)
	}
	// Strict lookup retains unknown resolution text as unresolved, while billing
	// normalization supplies the shared 480p default for a usable price key.
	require.Equal(t, VideoBillingResolution480P, NormalizeVideoBillingResolutionOrDefault("4k"))
	require.Equal(t, VideoBillingResolution1080P, NormalizeVideoBillingResolutionOrDefault("full_hd"))
}
