package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAI setup-token execution uses stored access tokens, while Spark shadows
// obtain their credentials from a validated parent. These cases verify the
// selected token and authentication kind, missing-token errors and rejection
// of setup-token records from the two tested foreign platforms.

// TestOpenAIGatewayServiceGetAccessTokenSetupToken supplies stored OpenAI
// setup credentials and a source that fails if called. Resolve returns oauth
// authentication directly, rejects a missing access token and rejects
// Anthropic or Grok setup-token records.
func TestOpenAIGatewayServiceGetAccessTokenSetupToken(t *testing.T) {
	svc := &OpenAIExecutionCredentials{OpenAI: func(context.Context, *Record) (string, error) {
		t.Fatal("setup-token 不应进入刷新源")
		return "", nil
	}}
	provider := &Record{
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeSetupToken,
		Credentials: map[string]any{"access_token": "setup-token-value"},
	}

	token, tokenType, err := svc.Resolve(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "setup-token-value", token)
	require.Equal(t, "oauth", tokenType)

	delete(provider.Credentials, "access_token")
	_, _, err = svc.Resolve(context.Background(), provider)
	require.EqualError(t, err, "access_token not found in credentials")

	for _, platform := range []string{capability.PlatformAnthropic, capability.PlatformGrok} {
		foreign := &Record{
			Platform:    platform,
			Type:        capability.ProviderTypeSetupToken,
			Credentials: map[string]any{"access_token": "foreign-token"},
		}
		_, _, err = svc.Resolve(context.Background(), foreign)
		require.EqualError(t, err, "unsupported provider type: setup-token")
	}
}

// TestGetAccessToken_SparkShadowResolvesToParent supplies a credential-free
// shadow and a parent reader for an OpenAI OAuth record. Resolve returns the
// stored parent token and oauth kind when no rotating token source is
// configured.
func TestGetAccessToken_SparkShadowResolvesToParent(t *testing.T) {
	ctx := context.Background()

	parentID := int64(100)
	parent := Record{
		ID:       parentID,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token": "parent-access-token",
		},
	}
	shadow := Record{
		ID:               200,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		// Credentials are intentionally absent on the shadow.
	}

	svc := &OpenAIExecutionCredentials{Parent: func(ctx context.Context, id int64) (*Record, error) {
		require.Equal(t, parentID, id)
		return &parent, nil
	}}

	// This fixture keeps the shadow empty and omits a token source so the result
	// identifies the parent reader's stored credential rather than a refresh callback.
	token, tokenType, err := svc.Resolve(ctx, &shadow)
	require.NoError(t, err)
	require.Equal(t, "parent-access-token", token)
	require.Equal(t, "oauth", tokenType)
}
