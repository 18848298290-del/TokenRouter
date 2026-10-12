package search

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	searchprovider "github.com/TokenFlux/TokenRouter/internal/search/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// configFixture controls overlapping reads and saves and can inject write failures.
type configFixture struct {
	mu        sync.Mutex
	raw       string
	fail      error
	firstRead atomic.Bool
	entered   chan struct{}
	release   chan struct{}
}

type configProxyFixture struct {
	first            atomic.Bool
	entered, release chan struct{}
}

func TestValidateWebSearchConfig_Nil(t *testing.T) {
	require.NoError(t, ValidateConfig(nil))
}

func TestValidateWebSearchConfig_Valid(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Type: "brave", QuotaLimit: searchQuotaFixture(1000)},
			{Type: "tavily", QuotaLimit: searchQuotaFixture(500)},
		},
	}
	require.NoError(t, ValidateConfig(cfg))
}

func TestValidateWebSearchConfig_TooManyProviders(t *testing.T) {
	cfg := &WebSearchEmulationConfig{Providers: make([]WebSearchProviderConfig, 11)}
	for i := range cfg.Providers {
		cfg.Providers[i] = WebSearchProviderConfig{Type: "brave"}
	}
	err := ValidateConfig(cfg)
	require.ErrorContains(t, err, "too many providers")
}

func TestValidateWebSearchConfig_InvalidType(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "bing"}},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "invalid type")
}

func TestValidateWebSearchConfig_NegativeQuotaLimit(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", QuotaLimit: searchQuotaFixture(-1)}},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "quota_limit must be > 0 or null")
}

func TestValidateWebSearchConfig_DuplicateType(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave"},
			{Type: "brave"},
		},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "duplicate type")
}

func TestValidateWebSearchConfig_NilQuotaLimit(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", QuotaLimit: nil}},
	}
	require.NoError(t, ValidateConfig(cfg))
}

func TestParseWebSearchConfigJSON_ValidJSON(t *testing.T) {
	raw := `{"enabled":true,"providers":[{"type":"brave","api_key":"sk-xxx"}]}`
	cfg := ParseConfig(raw)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Providers, 1)
	require.Equal(t, "brave", cfg.Providers[0].Type)
}

func TestParseWebSearchConfigJSON_EmptyString(t *testing.T) {
	cfg := ParseConfig("")
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Providers)
}

func TestParseWebSearchConfigJSON_InvalidJSON(t *testing.T) {
	cfg := ParseConfig("not{json")
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Providers)
}

func TestParseWebSearchConfigJSON_BackwardCompatibility(t *testing.T) {
	// Legacy priority and quota_refresh_interval fields are accepted as unknown JSON fields.
	raw := `{"enabled":true,"providers":[{"type":"brave","priority":1,"quota_refresh_interval":"monthly","quota_limit":1000}]}`
	cfg := ParseConfig(raw)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Providers, 1)
	require.Equal(t, int64(1000), *cfg.Providers[0].QuotaLimit)
}

func TestSanitizeWebSearchConfig_MaskAPIKey(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-secret-xxx"},
		},
	}
	out := SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.Equal(t, "", out.Providers[0].APIKey)
	require.True(t, out.Providers[0].APIKeyConfigured)
}

func TestSanitizeWebSearchConfig_NoAPIKey(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: ""}},
	}
	out := SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.Equal(t, "", out.Providers[0].APIKey)
	require.False(t, out.Providers[0].APIKeyConfigured)
}

func TestSanitizeWebSearchConfig_Nil(t *testing.T) {
	registry := NewRegistry()

	require.Nil(t, SanitizeWebSearchConfig(context.Background(), nil, registry))
}

func TestSanitizeWebSearchConfig_PreservesOtherFields(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "secret", QuotaLimit: searchQuotaFixture(1000)},
		},
	}
	out := SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.True(t, out.Enabled)
	require.Equal(t, int64(1000), *out.Providers[0].QuotaLimit)
}

func TestSanitizeWebSearchConfig_DoesNotMutateOriginal(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "secret"}},
	}
	_ = SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.Equal(t, "secret", cfg.Providers[0].APIKey)
}

func TestPopulateWebSearchUsage_NilInput(t *testing.T) {
	registry := NewRegistry()

	require.Nil(t, PopulateWebSearchUsage(context.Background(), nil, registry))
}

func TestPopulateWebSearchUsage_NoManager_QuotaUsedZero(t *testing.T) {
	registry := NewRegistry()

	// An empty registry leaves this fixture's zero usage unchanged.
	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-key", QuotaLimit: searchQuotaFixture(1000)},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.NotNil(t, out)
	require.Len(t, out.Providers, 1)
	require.Equal(t, int64(0), out.Providers[0].QuotaUsed)
}

func TestPopulateWebSearchUsage_APIKeyConfigured_True(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-key"},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.True(t, out.Providers[0].APIKeyConfigured)
}

func TestPopulateWebSearchUsage_APIKeyConfigured_False(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: ""},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.False(t, out.Providers[0].APIKeyConfigured)
}

func TestPopulateWebSearchUsage_NilQuotaLimit(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-key", QuotaLimit: nil},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.Nil(t, out.Providers[0].QuotaLimit)
}

func TestPopulateWebSearchUsage_NonNilQuotaLimit(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-key", QuotaLimit: searchQuotaFixture(500)},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.NotNil(t, out.Providers[0].QuotaLimit)
	require.Equal(t, int64(500), *out.Providers[0].QuotaLimit)
}

func TestPopulateWebSearchUsage_WithManager_NilRedis(t *testing.T) {
	registry := NewRegistry()

	// A Manager with no quota store reports zero usage.
	mgr := NewManager([]ProviderConfig{
		{Type: "brave", APIKey: "k"},
	}, nil, searchprovider.NewExecutor(), nil)
	registry.Set(mgr)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "sk-key", QuotaLimit: searchQuotaFixture(1000)},
		},
	}
	out := PopulateWebSearchUsage(context.Background(), cfg, registry)
	require.Equal(t, int64(0), out.Providers[0].QuotaUsed)
	require.True(t, out.Providers[0].APIKeyConfigured)
}

func TestPopulateWebSearchUsage_DoesNotMutateOriginal(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave", APIKey: "secret", QuotaLimit: searchQuotaFixture(100)},
		},
	}
	_ = PopulateWebSearchUsage(context.Background(), cfg, registry)
	// Populating usage leaves the caller's key and usage fields unchanged.
	require.Equal(t, "secret", cfg.Providers[0].APIKey)
	require.Equal(t, int64(0), cfg.Providers[0].QuotaUsed)
}

func TestResetWebSearchUsage_NilManager(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	err := ResetWebSearchUsage(context.Background(), "brave", registry)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized")
}

func TestConfigOldLoadCannotOverwriteSave(t *testing.T) {
	repo := &configFixture{raw: `{"enabled":false,"providers":[]}`, entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, nil, nil, nil)
	done := make(chan error, 1)
	go func() { _, err := service.GetWebSearchEmulationConfig(context.Background()); done <- err }()
	<-repo.entered
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture"}}}))
	close(repo.release)
	require.NoError(t, <-done)
	value, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, value.Enabled)
}

func TestConfigCopiesAndWriteFailure(t *testing.T) {
	repo := &configFixture{}
	service := NewConfigService(repo, nil, nil, nil)
	limit, sub, proxy, expiry := int64(10), int64(20), int64(30), int64(40)
	input := &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture", QuotaLimit: &limit, SubscribedAt: &sub, ProxyID: &proxy, ExpiresAt: &expiry}}}
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), input))
	input.Enabled = false
	input.Providers[0].APIKey = "changed"
	limit, sub, proxy, expiry = 1, 2, 3, 4
	value, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, value.Enabled)
	require.Equal(t, "fixture", value.Providers[0].APIKey)
	require.Equal(t, int64(10), *value.Providers[0].QuotaLimit)
	require.Equal(t, int64(20), *value.Providers[0].SubscribedAt)
	require.Equal(t, int64(30), *value.Providers[0].ProxyID)
	require.Equal(t, int64(40), *value.Providers[0].ExpiresAt)
	view := SanitizeWebSearchConfig(context.Background(), value, service.Registry())
	require.Empty(t, view.Providers[0].APIKey)
	*view.Providers[0].QuotaLimit = 99
	*value.Providers[0].ExpiresAt = 99
	current, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(10), *current.Providers[0].QuotaLimit)
	require.Equal(t, int64(40), *current.Providers[0].ExpiresAt)
	repo.mu.Lock()
	repo.fail = errors.New("write failed")
	repo.mu.Unlock()
	require.Error(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{}))
	current, err = service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, current.Enabled)
}

func TestOldManagerBuildCannotOverwriteSavedGeneration(t *testing.T) {
	repo := &configFixture{raw: `{"enabled":true,"providers":[{"type":"brave","api_key":"old","proxy_id":1}]}`}
	proxy := &configProxyFixture{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, proxy, func(c []ProviderConfig, g *WorkGroup) *Manager { return NewManager(c, nil, noSearchExecutor{}, g) }, nil)
	done := make(chan error, 1)
	go func() { done <- service.Initialize(context.Background()) }()
	<-proxy.entered
	id := int64(1)
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "new", ProxyID: &id}}}))
	close(proxy.release)
	require.NoError(t, <-done)
	require.Equal(t, "new", service.Registry().Get().ProviderConfigs()[0].APIKey)
	require.NoError(t, service.StopContext(context.Background()))
	require.Error(t, service.Initialize(context.Background()))
}

func TestLoadedConfigurationThenSavedReplacement(t *testing.T) {
	repo := &configFixture{raw: `{"enabled":false,"providers":[]}`}
	service := NewConfigService(repo, nil, func(c []ProviderConfig, g *WorkGroup) *Manager { return NewManager(c, nil, noSearchExecutor{}, g) }, nil)
	prior, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.False(t, prior.Enabled)
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "replacement"}}}))
	current, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, current.Enabled)
	require.Equal(t, "replacement", service.Registry().Get().ProviderConfigs()[0].APIKey)
}

func TestConsecutiveSavesKeepLastPublication(t *testing.T) {
	repo := &configFixture{}
	proxies := &configProxyFixture{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, proxies, func(c []ProviderConfig, g *WorkGroup) *Manager { return NewManager(c, nil, noSearchExecutor{}, g) }, nil)
	id := int64(1)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		first <- service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "first", ProxyID: &id}}})
	}()
	<-proxies.entered
	go func() {
		second <- service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "second", ProxyID: &id}}})
	}()
	close(proxies.release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	require.Equal(t, "second", service.Registry().Get().ProviderConfigs()[0].APIKey)
}

// searchQuotaFixture returns a pointer to a quota value.
func searchQuotaFixture(v int64) *int64 { return &v }

func (r *configFixture) GetValue(_ context.Context, _ string) (string, error) {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	if r.entered != nil && r.firstRead.CompareAndSwap(false, true) {
		close(r.entered)
		<-r.release
	}
	if raw == "" {
		return "", settings.ErrSettingNotFound
	}
	return raw, nil
}

func (r *configFixture) Set(_ context.Context, _, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.raw = value
	return nil
}

func (p *configProxyFixture) URLs(context.Context, []int64) (map[int64]string, error) {
	if p.first.CompareAndSwap(false, true) {
		close(p.entered)
		<-p.release
	}
	return map[int64]string{1: "http://fixture.invalid"}, nil
}
