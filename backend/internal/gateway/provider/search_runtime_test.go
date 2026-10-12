package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/search"
	searchprovider "github.com/TokenFlux/TokenRouter/internal/search/provider"
)

var (
	// webSearchToolBody contains a web_search tool.
	webSearchToolBody = []byte(`{"tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"test"}]}`)

	// nonWebSearchToolBody contains a non-search tool.
	nonWebSearchToolBody = []byte(`{"tools":[{"type":"text_editor"}],"messages":[{"role":"user","content":"test"}]}`)
)

// searchSettingRows returns the fixture's saved JSON configuration.
type searchSettingRows struct {
	search.ConfigRepository
	data string
}

type searchPricingConfigRows struct {
	routing.PricingConfigRepository
	pricingConfigs []testkit.Configuration
}

func (r searchSettingRows) GetValue(context.Context, string) (string, error) { return r.data, nil }

func newSearchSettingsFixture(enabled bool, registry *search.Registry) *search.ConfigService {
	value := &search.WebSearchEmulationConfig{Enabled: enabled, Providers: []search.WebSearchProviderConfig{{Type: "brave", APIKey: "sk-test"}}}
	data, _ := json.Marshal(value)
	return search.NewConfigService(searchSettingRows{data: string(data)}, nil, nil, registry)
}

func (r searchPricingConfigRows) ListAll(context.Context) ([]testkit.Configuration, error) {
	return r.pricingConfigs, nil
}

func (r searchPricingConfigRows) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

func newPricingConfigServiceWithCache(groupID int64, ch *testkit.Configuration) *routing.PricingConfigService {
	value := ch.Clone()
	value.GroupIDs = []int64{groupID}
	return testkit.NewPricingConfigService(searchPricingConfigRows{pricingConfigs: []testkit.Configuration{*value}}, nil, routing.PricingConfigOptions{Now: time.Now})
}

// newSearchProviderPolicy creates a provider with the given search-emulation mode.
func newSearchProviderPolicy(mode string) *searchtools.ProviderPolicy {
	return &searchtools.ProviderPolicy{
		ID:       1,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{searchtools.FeatureKey: mode},
	}
}

func TestShouldEmulateWebSearch_NilManager(t *testing.T) {
	registry := search.NewRegistry()
	registry.Set(nil)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)

	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeEnabled)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_NotOnlyWebSearchTool(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)

	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeEnabled)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: nonWebSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_GlobalDisabled(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	// The global setting disables search emulation.

	settingSvc := newSearchSettingsFixture(false, registry)
	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeEnabled)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_AccountDisabled(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeDisabled)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_ProviderEnabled(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeEnabled)
	require.True(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_DefaultMode_GroupPolicyEnabled(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	ch := &testkit.Configuration{
		ID:     10,
		Status: billing.StatusActive,
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{capability.PlatformAnthropic: true},
		},
	}
	pricingConfigSvc := newPricingConfigServiceWithCache(42, ch)
	svc := NewSearchTools(settingSvc, pricingConfigSvc)

	provider := newSearchProviderPolicy(searchtools.ModeDefault)
	groupID := int64(42)
	require.True(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: &groupID}))
}

func TestShouldEmulateWebSearch_DefaultMode_GroupPolicyDisabled(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	ch := &testkit.Configuration{
		ID:     10,
		Status: billing.StatusActive,
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{capability.PlatformAnthropic: false},
		},
	}
	pricingConfigSvc := newPricingConfigServiceWithCache(42, ch)
	svc := NewSearchTools(settingSvc, pricingConfigSvc)

	provider := newSearchProviderPolicy(searchtools.ModeDefault)
	groupID := int64(42)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: &groupID}))
}

func TestShouldEmulateWebSearch_DefaultMode_NilGroupID(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeDefault)
	// Default mode disables emulation when no group is supplied.
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

func TestShouldEmulateWebSearch_DefaultMode_NilPricingConfigService(t *testing.T) {
	mgr := search.NewManager([]search.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil, searchprovider.NewExecutor(), nil)
	registry := search.NewRegistry()
	registry.Set(mgr)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)
	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeDefault)
	groupID := int64(42)
	// Default mode disables emulation when pricing configuration is unavailable.
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: &groupID}))
}
