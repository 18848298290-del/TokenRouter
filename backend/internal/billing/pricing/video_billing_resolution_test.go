package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
	// Runtime billing still selects a tier when the upstream resolution is unrecognized.
	require.Equal(t, VideoBillingResolution480P, NormalizeVideoBillingResolutionOrDefault("4k"))
	require.Equal(t, VideoBillingResolution1080P, NormalizeVideoBillingResolutionOrDefault("full_hd"))
}
