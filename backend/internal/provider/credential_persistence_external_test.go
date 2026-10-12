package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Spark shadow authentication belongs to the parent provider, so an ordinary
// credential write must skip the shadow. The fixture inspects both the passed
// record and its stored row after trying to supply new authentication values.

// TestPersistProviderCredentials_SkipsShadow attempts an ordinary credential
// write against a stored Spark shadow. The call succeeds while both the input
// and stored credential maps remain empty.
func TestPersistProviderCredentials_SkipsShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	parentID := int64(1)
	shadow := &provider.Record{
		Name: "shadow", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{},
		ParentProviderID: &parentID, QuotaDimension: provider.QuotaDimensionSpark,
	}
	require.NoError(t, repo.Create(ctx, shadow))

	_, err := provider.PersistCredentials(ctx, repo, shadow, map[string]any{"access_token": "LEAK", "refresh_token": "LEAK"}, nil)
	require.NoError(t, err)
	require.Empty(t, shadow.Credentials, "影子凭据不可被写入(传入对象)")
	require.Empty(t, repo.providers[shadow.ID].Credentials, "影子凭据不可被写入(仓储)")
}
