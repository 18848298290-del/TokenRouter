package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

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

// TestGetAccessToken_SparkShadowResolvesToParent checks parent credential resolution for a shadow.
// The shadow has no credentials of its own.
// The returned access token must come from the parent record.
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

	// The parent reader supplies the shadow's credentials.
	// With no token source configured, Resolve returns the stored parent token.
	token, tokenType, err := svc.Resolve(ctx, &shadow)
	require.NoError(t, err)
	require.Equal(t, "parent-access-token", token)
	require.Equal(t, "oauth", tokenType)
}
