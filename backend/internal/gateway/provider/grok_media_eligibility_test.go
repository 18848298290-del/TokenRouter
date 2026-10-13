package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// grokMediaEligibilityProberStub records probe calls and returns configured
// eligibility observations.
type grokMediaEligibilityProberStub struct {
	eligible bool   // configured probe eligibility result
	reason   string // configured probe reason text
	err      error  // configured probe failure result
	calls    int    // observed probe invocation count
}

// ProbeMediaEligibility increments the fixture call count and returns its
// configured eligibility, reason, and error. The fixture ignores lookup
// parameters so tests can isolate whether probing occurs.
func (s *grokMediaEligibilityProberStub) ProbeMediaEligibility(context.Context, int64) (bool, string, error) {
	s.calls++
	return s.eligible, s.reason, s.err
}

// TestEnsureGrokMediaProviderEligibility compares API Key eligibility with
// OAuth billing observations that require probing. API Keys skip probing,
// successful probes return eligibility, and absent or failing probes retain a
// rejecting result.
func TestEnsureGrokMediaProviderEligibility(t *testing.T) {
	t.Run("non oauth provider does not probe", func(t *testing.T) {
		prober := &grokMediaEligibilityProberStub{}

		provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}}

		eligible, reason, err := CheckGrokMediaEligibility(context.Background(), provider, prober)

		require.NoError(t, err)
		require.True(t, eligible)
		require.Equal(t, "non_oauth", reason)
		require.Zero(t, prober.calls)
	})

	t.Run("unobserved oauth is probed before forwarding", func(t *testing.T) {
		prober := &grokMediaEligibilityProberStub{eligible: true, reason: "eligible"}

		provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 7, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

		eligible, reason, err := CheckGrokMediaEligibility(context.Background(), provider, prober)

		require.NoError(t, err)
		require.True(t, eligible)
		require.Equal(t, "eligible", reason)
		require.Equal(t, 1, prober.calls)
	})

	t.Run("missing prober fails closed", func(t *testing.T) {
		var prober GrokMediaEligibilityProber
		provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 8, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

		eligible, reason, err := CheckGrokMediaEligibility(context.Background(), provider, prober)

		require.Error(t, err)
		require.False(t, eligible)
		require.Equal(t, "billing_probe_unavailable", reason)
	})

	t.Run("probe failure fails closed", func(t *testing.T) {
		probeErr := errors.New("probe failed")
		prober := &grokMediaEligibilityProberStub{reason: "billing_unobserved", err: probeErr}

		provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

		eligible, reason, err := CheckGrokMediaEligibility(context.Background(), provider, prober)

		require.ErrorIs(t, err, probeErr)
		require.False(t, eligible)
		require.Equal(t, "billing_unobserved", reason)
	})
}
