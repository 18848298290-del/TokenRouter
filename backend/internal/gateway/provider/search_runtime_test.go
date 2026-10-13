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
	// These request fixtures distinguish web_search from text_editor declarations
	// while retaining ordinary user input, so policy tests select their intended tool shape.
	webSearchToolBody = []byte(`{"tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"test"}]}`)

	// nonWebSearchToolBody contains a non-search tool.
	nonWebSearchToolBody = []byte(`{"tools":[{"type":"text_editor"}],"messages":[{"role":"user","content":"test"}]}`)
)

// searchSettingRows supplies encoded search configuration to the test runtime
// settings reader.
type searchSettingRows struct {
	search.ConfigRepository        // unused config repository operations
	data                    string // encoded search configuration JSON
}

// searchPricingConfigRows supplies group-feature configuration fixtures to
// routing policy reads.
type searchPricingConfigRows struct {
	routing.PricingConfigRepository // unused routing repository operations
	// configured group-feature fixture rows
	pricingConfigs []testkit.Configuration
}

// GetValue returns the fixture JSON configuration regardless of the setting
// key.
func (r searchSettingRows) GetValue(context.Context, string) (string, error) { return r.data, nil }

// newSearchSettingsFixture constructs search runtime with a fixed Brave
// provider and enabled flag. It binds the supplied registry so tests control
// manager availability independently of encoded global configuration.
func newSearchSettingsFixture(enabled bool, registry *search.Registry) *search.ConfigService {
	value := &search.WebSearchEmulationConfig{Enabled: enabled, Providers: []search.WebSearchProviderConfig{{Type: "brave", APIKey: "sk-test"}}}
	// The fixed fixture contains scalar fields, nil integer pointers, and a
	// provider slice; it has no unsupported values or cyclic references that can fail JSON encoding.
	data, _ := json.Marshal(value)
	return search.NewConfigService(searchSettingRows{data: string(data)}, nil, nil, registry)
}

// ListAll returns the fixture configuration slice by reference for routing
// policy tests. Callers keep its entries stable while the pricing service
// reads and captures group feature configuration.
func (r searchPricingConfigRows) ListAll(context.Context) ([]testkit.Configuration, error) {
	return r.pricingConfigs, nil
}

// GetGroupPlatforms returns an empty platform map for the group-policy
// fixture. The input group list does not affect this result, allowing tests to
// focus on their configured feature flags.
func (r searchPricingConfigRows) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

// newPricingConfigServiceWithCache clones a fixture configuration and attaches
// it to groupID before binding the test service. The caller input remains
// available while the clone supplies group-feature policy.
func newPricingConfigServiceWithCache(groupID int64, ch *testkit.Configuration) *routing.PricingConfigService {
	value := ch.Clone()
	value.GroupIDs = []int64{groupID}
	return testkit.NewPricingConfigService(searchPricingConfigRows{pricingConfigs: []testkit.Configuration{*value}}, nil, routing.PricingConfigOptions{Now: time.Now})
}

// newSearchProviderPolicy creates an Anthropic API Key fixture with the
// supplied emulation mode.
func newSearchProviderPolicy(mode string) *searchtools.ProviderPolicy {
	return &searchtools.ProviderPolicy{
		ID:       1,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{searchtools.FeatureKey: mode},
	}
}

// TestShouldEmulateWebSearch_NilManager enables settings and provider policy
// while publishing no manager. The emulator still returns false because search
// execution is unavailable.
func TestShouldEmulateWebSearch_NilManager(t *testing.T) {
	registry := search.NewRegistry()
	registry.Set(nil)
	defer registry.Set(nil)

	settingSvc := newSearchSettingsFixture(true, registry)

	svc := NewSearchTools(settingSvc, nil)
	provider := newSearchProviderPolicy(searchtools.ModeEnabled)
	require.False(t, svc.ShouldEmulate(context.Background(), searchtools.PolicyInput{Body: webSearchToolBody, Mode: SearchProviderMode(provider), Platform: provider.Platform, GroupID: nil}))
}

// TestShouldEmulateWebSearch_NotOnlyWebSearchTool publishes an active search
// manager but supplies a text-editor tool request. Policy evaluation returns
// false despite enabled global settings and provider mode.
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

// TestShouldEmulateWebSearch_GlobalDisabled publishes a search manager and
// provider-enabled mode with the global search setting disabled. The
// web-search request remains denied by emulation's global gate.
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

// TestShouldEmulateWebSearch_AccountDisabled publishes a search manager with
// enabled global settings but provider-disabled mode. That override makes
// emulation false for the supplied web-search tool request.
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

// TestShouldEmulateWebSearch_ProviderEnabled publishes a manager with enabled
// global and provider modes plus a web-search request. Emulation is permitted
// with no group policy.
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

// TestShouldEmulateWebSearch_DefaultMode_GroupPolicyEnabled publishes search
// and enables the Anthropic group feature for provider default mode. A request
// in that group qualifies for emulation.
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

// TestShouldEmulateWebSearch_DefaultMode_GroupPolicyDisabled supplies provider
// default mode and an explicitly disabled group feature. Emulation returns
// false despite the manager and global enablement.
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

// TestShouldEmulateWebSearch_DefaultMode_NilGroupID supplies enabled global
// search, an active manager, and default provider mode with no group ID.
// Missing group policy keeps the web-search request ineligible for emulation.
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

// TestShouldEmulateWebSearch_DefaultMode_NilPricingConfigService supplies a
// group ID but no group-policy service under default provider mode. Emulation
// remains disabled when that policy cannot be read.
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
