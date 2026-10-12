package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
)

// Bedrock providers may omit aws_region while still supplying a complete key pair.
// This case exercises bedrock_signer.go and checks that successful construction
// uses the platform's default runtime region, so an unspecified region produces
// a usable signer with a defined signing scope.

// TestNewBedrockSignerFromProvider_DefaultRegion constructs a signer from a
// key pair
// with no region credential. It checks successful creation and the platform's
// default signing region on the returned signer.
func TestNewBedrockSignerFromProvider_DefaultRegion(t *testing.T) {
	provider := &provider.Record{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeBedrock,
		Credentials: map[string]any{
			"aws_access_key_id":     "test-akid",
			"aws_secret_access_key": "test-secret",
		},
	}

	signer, err := NewBedrockSignerFromProvider(provider)
	require.NoError(t, err)
	require.NotNil(t, signer)
	assert.Equal(t, bedrock.DefaultBedrockRegion, signer.Region)
}
