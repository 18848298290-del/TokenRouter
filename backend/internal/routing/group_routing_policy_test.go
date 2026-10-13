package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Group admission rules remain usable while shared pricing cards, billing
// source, and configuration status change. An independent group reader
// supplies the mapping, allowlist, and feature flag, so price invalidation
// can be exercised separately from policy edits.

// policyPriceStore supplies shared pricing rows while an independent callback
// reads group policies.
type policyPriceStore struct {
	// PricingConfigRepository is embedded to satisfy unused methods; invoking
	// one of those methods through the nil fixture interface would panic.
	PricingConfigRepository // unused repository method placeholders
	// configs is returned directly by ListAll. Tests may change its contents
	// between synchronous cache invalidations to supply the next snapshot's
	// input.
	configs []PricingConfig // supplied shared pricing rows
}

// ListAll returns the fixture's configuration slice directly with no error.
// The service copies rows for its cache, and tests mutate this input between
// completed lookups.
func (s *policyPriceStore) ListAll(context.Context) ([]PricingConfig, error) { return s.configs, nil }

// GetGroupPlatforms supplies OpenAI labels for groups 1 and 2.
func (s *policyPriceStore) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{1: PlatformOpenAI, 2: PlatformOpenAI}, nil
}

// TestPricingChangesDoNotChangeGroupPolicy mutates price cards, billing
// source, status, and configuration presence while reading independent group
// rules. Mapping, allowlist, and web search stay intact until one group's
// mapping is edited.
func TestPricingChangesDoNotChangeGroupPolicy(t *testing.T) {
	store := &policyPriceStore{configs: []PricingConfig{{ID: 9, Status: StatusActive, GroupIDs: []int64{1, 2}, BillingModelSource: BillingModelSourceRequested}}}
	groups := map[int64]*Group{}
	for _, id := range []int64{1, 2} {
		groups[id] = &Group{ID: id, RoutingPolicy: GroupRoutingPolicy{
			Enabled: true, RestrictModels: true, RestrictionModelSource: BillingModelSourceGroupMapped,
			AllowedModels:  []string{"gpt-allowed"},
			ModelMapping:   map[string]string{"alias": "gpt-allowed"},
			FeaturesConfig: map[string]any{"web_search_emulation": map[string]any{PlatformOpenAI: true}},
		}}
	}
	service := NewPricingConfigService(store, nil, PricingConfigOptions{ReadGroup: func(_ context.Context, id int64) (*Group, error) { return groups[id], nil }})
	ctx := context.Background()
	assertPolicy := func(id int64) {
		t.Helper()
		result := service.ResolveGroupMapping(ctx, id, "alias")
		require.Equal(t, "gpt-allowed", result.MappedModel)
		require.Equal(t, BillingModelSourceGroupMapped, result.RestrictionModelSource)
		require.False(t, service.IsModelRestricted(ctx, id, "gpt-allowed"))
		require.True(t, service.IsModelRestricted(ctx, id, "gpt-denied"))
		policy, err := service.GetGroupPolicy(ctx, id)
		require.NoError(t, err)
		require.True(t, policy.IsWebSearchEmulationEnabled(PlatformOpenAI))
	}
	assertPolicy(1)
	store.configs[0].ModelPricing = []ModelPricingEntry{{Models: []string{"gpt-denied"}}}
	store.configs[0].BillingModelSource = BillingModelSourceUpstream
	// Rebuild prices after changing the input row so subsequent policy
	// assertions exercise the changed pricing snapshot while the separate group
	// reader keeps supplying its rules.
	service.InvalidateCache()
	assertPolicy(1)
	store.configs[0].Status = StatusDisabled
	// Publish disabled pricing before the next policy check, so group admission
	// is exercised while the shared configuration no longer supplies active
	// billing metadata.
	service.InvalidateCache()
	assertPolicy(1)
	store.configs = nil
	// Remove pricing associations before checking both groups, making their
	// surviving mappings and allowlists come from the independent group reader.
	service.InvalidateCache()
	assertPolicy(1)
	assertPolicy(2)
	groups[1].RoutingPolicy.ModelMapping["alias"] = "changed"
	require.Equal(t, "changed", service.ResolveGroupMapping(ctx, 1, "alias").MappedModel)
	require.Equal(t, "gpt-allowed", service.ResolveGroupMapping(ctx, 2, "alias").MappedModel)
}

// TestGroupPolicyAllowlistAndSnapshot mutates a cloned allowlist and nested
// feature object, then checks the original values. It also checks
// case-insensitive wildcard admission, rejection by an empty enabled
// allowlist, and unrestricted disabled checks.
func TestGroupPolicyAllowlistAndSnapshot(t *testing.T) {
	policy := GroupRoutingPolicy{Enabled: true, RestrictModels: true, AllowedModels: []string{"claude-sonnet-*"}, FeaturesConfig: map[string]any{"nested": []any{map[string]any{"enabled": true}}}}
	copy := policy.Clone()
	copy.AllowedModels[0] = "changed"
	copiedItems, ok := copy.FeaturesConfig["nested"].([]any)
	require.True(t, ok)
	copiedObject, ok := copiedItems[0].(map[string]any)
	require.True(t, ok)
	copiedObject["enabled"] = false
	require.Equal(t, "claude-sonnet-*", policy.AllowedModels[0])
	originalItems, ok := policy.FeaturesConfig["nested"].([]any)
	require.True(t, ok)
	originalObject, ok := originalItems[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, originalObject["enabled"])
	view := &GroupPolicyView{GroupRoutingPolicy: policy}
	require.False(t, view.IsModelRestricted("Claude-Sonnet-4.6"))
	require.True(t, view.IsModelRestricted("claude-opus-4-6"))
	require.False(t, view.IsModelRestricted("claude-sonnet-4-6"))
	view.AllowedModels = nil
	require.True(t, view.IsModelRestricted("anything"))
	view.RestrictModels = false
	require.False(t, view.IsModelRestricted("anything"))
}

// TestGroupPolicyReadFailureDoesNotAllowRequests injects a group read error
// and malformed saved policy JSON. The service rejects the model, and
// decoding reports Enabled and RestrictModels.
func TestGroupPolicyReadFailureDoesNotAllowRequests(t *testing.T) {
	service := NewPricingConfigService(&policyPriceStore{}, nil, PricingConfigOptions{ReadGroup: func(context.Context, int64) (*Group, error) { return nil, errors.New("database unavailable") }})
	require.True(t, service.IsModelRestricted(context.Background(), 1, "model"))
	corrupt := DecodeGroupRoutingPolicy([]byte(`{"enabled":`))
	require.True(t, corrupt.Enabled)
	require.True(t, corrupt.RestrictModels)
}
