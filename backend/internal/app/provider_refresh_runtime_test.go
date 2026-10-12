package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// TestTokenRefreshService_RegistrationsAreCandidateEligibilitySource checks one complete registration per supported platform.
func TestTokenRefreshService_RegistrationsAreCandidateEligibilitySource(t *testing.T) {
	registrations := provideRefreshPlatforms(nil, nil, nil, &provider.AntigravityAuthorization{}, provideradapter.NewQoderAuthorization(nil, nil), nil, nil, nil)
	platforms := make([]string, 0, len(registrations))
	require.Len(t, registrations, 6)
	for _, registration := range registrations {
		platforms = append(platforms, registration.Platform)
		require.NotNil(t, registration.Refresher)
		require.NotNil(t, registration.Executor)
	}
	require.Equal(t, []string{provider.PlatformAnthropic, provider.PlatformOpenAI, provider.PlatformGemini, provider.PlatformAntigravity, provider.PlatformQoder, provider.PlatformGrok}, platforms)
}
