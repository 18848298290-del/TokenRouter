package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestCreateProviderDiscardsDeprecatedBillingProbeExtra creates an API Key
// provider containing obsolete billing-probe values. Creation removes both
// retired keys while retaining the unrelated custom configuration supplied in
// the same Extra object.
func TestCreateProviderDiscardsDeprecatedBillingProbeExtra(t *testing.T) {
	repo := &providerServiceTestRepo{}
	created, err := newProviderEditorForTest(repo).CreateProvider(context.Background(), &provider.CreateProviderInput{
		Name:        "upstream",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"upstream_billing_probe_enabled": true,
			"upstream_billing_probe":         map[string]any{"status": "ok"},
			"custom":                         "value",
		},
	})

	require.NoError(t, err)
	require.NotContains(t, created.Extra, "upstream_billing_probe_enabled")
	require.NotContains(t, created.Extra, "upstream_billing_probe")
	require.Equal(t, "value", created.Extra["custom"])
}
