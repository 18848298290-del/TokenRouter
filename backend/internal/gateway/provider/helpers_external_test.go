package provider_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// fastModeTestContext binds the requested Fast policy, group 11, and model to
// a fresh context. It provides the policy inputs used by external Fast tests
// without an authenticated Key record.
func fastModeTestContext(policy, model string) context.Context {
	ctx := apikey.WithFastModePolicy(context.Background(), policy)
	ctx = requeststate.WithGroup(ctx, &routing.Group{ID: 11})
	return context.WithValue(ctx, telemetry.Model, model)
}

// fastModeTestResolver builds billing prices from a fixed two-model catalogue
// snapshot. Both models advertise service-tier and prompt-cache pricing,
// giving Fast policy tests predictable price capability checks.
func fastModeTestResolver() *billingcore.PriceResolver {
	pricing := catalogprovider.NewServiceFromSnapshot(catalogprovider.Options{ModelLookupCandidates: modelidentity.CandidatesFactory}, nil, catalogprovider.Snapshot{Data: map[string]*billingpricing.CatalogModelPricing{
		"gpt-5.5": {
			InputCostPerToken:     5e-6,
			OutputCostPerToken:    30e-6,
			SupportsServiceTier:   true,
			SupportsPromptCaching: true,
		},
		"claude-opus-4-8": {
			InputCostPerToken:     5e-6,
			OutputCostPerToken:    25e-6,
			SupportsServiceTier:   true,
			SupportsPromptCaching: true,
		},
	}})
	billing := billingtestkit.Calculator(pricing, nil)
	return billingtestkit.PriceResolver(nil, billing)
}

// newFastPolicyContract builds an execution policy backed by fixture runtime
// settings. Nil input leaves settings empty for default resolution; nonnil
// settings are JSON-encoded, and serialization errors fail the test.
func newFastPolicyContract(t *testing.T, value *tierpolicy.OpenAIFastPolicySettings) *gatewayprovider.ExecutionFastPolicy {
	t.Helper()
	repo := &gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}
	if value != nil {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		repo.Values[gateway.SettingKeyOpenAIFastPolicySettings] = string(raw)
	}
	return &gatewayprovider.ExecutionFastPolicy{Readers: gatewaytestkit.RuntimeReaders(settings.New(repo))}
}
