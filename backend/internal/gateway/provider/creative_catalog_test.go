package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type creativeCatalogRows struct{ values []creative.CatalogProvider }

func (s creativeCatalogRows) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]creative.CatalogProvider, error) {
	return s.values, nil
}

// TestCreativeCatalogUsesExecutionModelPolicy checks that catalog and execution both apply mappings with passthrough enabled.
func TestCreativeCatalogUsesExecutionModelPolicy(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		value := &provider.Record{
			Platform: creative.PlatformOpenAI, Type: "apikey", Status: "active", Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-image-1": "gpt-image-2"}},
			Extra:       map[string]any{"openai_passthrough": passthrough},
		}
		public := creative.Public{ProviderRepo: creativeCatalogRows{[]creative.CatalogProvider{CreativeCatalogProvider(value)}}}
		group := &creative.GroupView{ID: 12, Operations: creative.OperationsForGroup(false, nil), RoutingPolicy: routing.GroupRoutingPolicy{
			Enabled: true, RestrictModels: true, RestrictionModelSource: routing.BillingModelSourceUpstream,
			AllowedModels: []string{"gpt-image-2"},
		}}
		models, err := public.CreativeModelsForGroup(context.Background(), group)
		require.NoError(t, err)
		require.Equal(t, "gpt-image-2", models["gpt-image-1"])
	}
}
