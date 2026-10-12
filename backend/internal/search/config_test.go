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

// Search settings distinguish validation, secret-bearing administrator views,
// and the Manager installed for execution. Small cases check decoded flags and
// optional quota values, while publication scenarios retain copies to reveal
// mutation leaking into the cache.
//
// Read and proxy fixtures can pause their first calls after an earlier
// generation is captured. Saves proceed while those operations are held, and
// channel joins reveal which generation remains published when the old work
// returns. A write-failure control checks that this save does not publish its
// input. These fixtures use in-memory settings and inert executors, so their
// assertions establish local publication ordering rather than distributed
// storage behavior.

// configFixture supplies gated settings reads and optional write failures for
// publication tests.
type configFixture struct {
	// mu protects raw and fail while reads and saves overlap. GetValue snapshots
	// raw before its gate so the held response can represent an older generation.
	mu   sync.Mutex // stored fixture value lock
	raw  string     // serialized fixture search setting
	fail error      // injected settings write failure
	// firstRead selects the single read that closes entered and waits for release.
	// Set both gate channels before starting concurrent test operations.
	firstRead atomic.Bool   // first gated read claim
	entered   chan struct{} // first-read entry notification
	release   chan struct{} // first-read continuation gate
}

// configProxyFixture pauses its first address resolution to expose overlapping
// Manager builds.
type configProxyFixture struct {
	// first chooses the one URLs call that closes entered and waits for release.
	// Configure both channels before initialization or saves start their
	// overlapping builds.
	first            atomic.Bool   // first gated resolution claim
	entered, release chan struct{} // entered: first-call entry notification; release: first-call continuation gate
}

// TestValidateWebSearchConfig_Nil passes absent configuration to structural
// validation. It expects nil to be accepted, leaving Save's separate nonnil
// input requirement outside this validation call.
func TestValidateWebSearchConfig_Nil(t *testing.T) {
	require.NoError(t, ValidateConfig(nil))
}

// TestValidateWebSearchConfig_Valid submits enabled Brave and Tavily entries
// with different positive limits. Structural validation accepts this supported,
// nonduplicated supplier configuration before save-time credential checks.
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

// TestValidateWebSearchConfig_TooManyProviders submits eleven repeated supplier
// entries to exceed the list cap. The returned error identifies excessive count
// before duplicate-type validation is reached.
func TestValidateWebSearchConfig_TooManyProviders(t *testing.T) {
	cfg := &WebSearchEmulationConfig{Providers: make([]WebSearchProviderConfig, 11)}
	for i := range cfg.Providers {
		cfg.Providers[i] = WebSearchProviderConfig{Type: "brave"}
	}
	err := ValidateConfig(cfg)
	require.ErrorContains(t, err, "too many providers")
}

// TestValidateWebSearchConfig_InvalidType submits a Bing entry outside the two
// supported literal supplier types. Validation reports the invalid type instead
// of admitting the configuration for saving.
func TestValidateWebSearchConfig_InvalidType(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "bing"}},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "invalid type")
}

// TestValidateWebSearchConfig_NegativeQuotaLimit submits a negative pointed-to
// limit for a supported supplier. The asserted error rejects that quota input
// before it can become runtime unlimited behavior.
func TestValidateWebSearchConfig_NegativeQuotaLimit(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", QuotaLimit: searchQuotaFixture(-1)}},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "quota_limit must be > 0 or null")
}

// TestValidateWebSearchConfig_DuplicateType submits two Brave entries with the
// same literal type. Validation reports a duplicate because configuration
// storage and quota keys identify suppliers by type.
func TestValidateWebSearchConfig_DuplicateType(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{
			{Type: "brave"},
			{Type: "brave"},
		},
	}
	require.ErrorContains(t, ValidateConfig(cfg), "duplicate type")
}

// TestValidateWebSearchConfig_NilQuotaLimit submits a supported supplier with
// an absent quota pointer. Validation accepts that optional input, which
// publication converts into the Manager's unlimited quota value.
func TestValidateWebSearchConfig_NilQuotaLimit(t *testing.T) {
	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", QuotaLimit: nil}},
	}
	require.NoError(t, ValidateConfig(cfg))
}

// TestParseWebSearchConfigJSON_ValidJSON decodes enabled configuration
// containing one Brave supplier. The assertions retain the enable flag,
// provider count, and supplier type from the stored JSON input.
func TestParseWebSearchConfigJSON_ValidJSON(t *testing.T) {
	raw := `{"enabled":true,"providers":[{"type":"brave","api_key":"sk-xxx"}]}`
	cfg := ParseConfig(raw)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Providers, 1)
	require.Equal(t, "brave", cfg.Providers[0].Type)
}

// TestParseWebSearchConfigJSON_EmptyString parses a setting value with no JSON
// text. It receives disabled configuration and an empty supplier list, matching
// the absent-settings representation.
func TestParseWebSearchConfigJSON_EmptyString(t *testing.T) {
	cfg := ParseConfig("")
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Providers)
}

// TestParseWebSearchConfigJSON_InvalidJSON parses malformed stored text through
// the tolerant reader. The returned configuration is disabled with no
// providers, giving runtime publication an empty candidate list.
func TestParseWebSearchConfigJSON_InvalidJSON(t *testing.T) {
	cfg := ParseConfig("not{json")
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Providers)
}

// TestParseWebSearchConfigJSON_BackwardCompatibility includes retired priority
// and refresh fields alongside supported configuration values. Decoding keeps
// enablement, the supplier entry, and its limit while tolerating those unknown
// fields.
func TestParseWebSearchConfigJSON_BackwardCompatibility(t *testing.T) {
	// Keep retired fields in the stored JSON fixture to protect tolerant decoding
	// of supported values when older configuration payloads remain persisted.
	raw := `{"enabled":true,"providers":[{"type":"brave","priority":1,"quota_refresh_interval":"monthly","quota_limit":1000}]}`
	cfg := ParseConfig(raw)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Providers, 1)
	require.Equal(t, int64(1000), *cfg.Providers[0].QuotaLimit)
}

// TestSanitizeWebSearchConfig_MaskAPIKey decorates a configuration containing a
// supplier secret with an empty registry. The result clears the key and reports
// its configured-presence flag as true.
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

// TestSanitizeWebSearchConfig_NoAPIKey decorates an entry whose key starts
// empty. The sanitized response keeps that empty key and reports false for key
// presence in the administrator view.
func TestSanitizeWebSearchConfig_NoAPIKey(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: ""}},
	}
	out := SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.Equal(t, "", out.Providers[0].APIKey)
	require.False(t, out.Providers[0].APIKeyConfigured)
}

// TestSanitizeWebSearchConfig_Nil passes absent configuration through the
// sanitizer with a valid registry. It expects nil back rather than creating a
// configuration that could imply stored settings exist.
func TestSanitizeWebSearchConfig_Nil(t *testing.T) {
	registry := NewRegistry()

	require.Nil(t, SanitizeWebSearchConfig(context.Background(), nil, registry))
}

// TestSanitizeWebSearchConfig_PreservesOtherFields sanitizes enabled
// configuration carrying a positive quota pointer. The assertions retain
// enablement and the configured limit while the secret-clearing operation
// decorates the copied view.
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

// TestSanitizeWebSearchConfig_DoesNotMutateOriginal sanitizes a caller-owned
// entry containing a secret. The caller's key stays intact after the response
// copy has been prepared for sanitized consumers.
func TestSanitizeWebSearchConfig_DoesNotMutateOriginal(t *testing.T) {
	registry := NewRegistry()

	cfg := &WebSearchEmulationConfig{
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "secret"}},
	}
	_ = SanitizeWebSearchConfig(context.Background(), cfg, registry)
	require.Equal(t, "secret", cfg.Providers[0].APIKey)
}

// TestPopulateWebSearchUsage_NilInput passes absent configuration to usage
// decoration with a valid registry. It expects nil back so a missing
// configuration stays distinguishable from an empty configured value.
func TestPopulateWebSearchUsage_NilInput(t *testing.T) {
	registry := NewRegistry()

	require.Nil(t, PopulateWebSearchUsage(context.Background(), nil, registry))
}

// TestPopulateWebSearchUsage_NoManager_QuotaUsedZero decorates one supplier
// against an empty registry. The response remains nonnil with that provider and
// retains this fixture's initial zero usage value.
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

// TestPopulateWebSearchUsage_APIKeyConfigured_True decorates a supplier with a
// nonempty key and no Manager. The response reports configured credentials from
// the supplied key's presence independently of usage storage.
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

// TestPopulateWebSearchUsage_APIKeyConfigured_False decorates a supplier whose
// key is exactly empty. The response reports false for configured credentials
// while usage decoration still returns the provider entry.
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

// TestPopulateWebSearchUsage_NilQuotaLimit decorates a keyed entry with an
// absent quota pointer. The output retains nil, keeping unlimited configuration
// distinguishable from a supplied numeric limit.
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

// TestPopulateWebSearchUsage_NonNilQuotaLimit decorates an entry with a
// pointed-to limit of five hundred. The output retains a nonnil pointer and
// that value after usage view construction.
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

// TestPopulateWebSearchUsage_WithManager_NilRedis publishes a Manager without
// quota storage before decorating configuration. Returned usage is zero, and
// key presence is derived from the supplied configuration's nonempty secret.
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

// TestPopulateWebSearchUsage_DoesNotMutateOriginal decorates a caller-owned
// keyed supplier with no Manager. Assertions retain its secret and zero usage
// after the independent response copy has been built.
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

// TestResetWebSearchUsage_NilManager requests a counter reset through an empty
// registry. It receives an initialization error instead of reporting success
// for an unavailable search runtime.
func TestResetWebSearchUsage_NilManager(t *testing.T) {
	registry := NewRegistry()

	registry.Set(nil)
	defer registry.Set(nil)

	err := ResetWebSearchUsage(context.Background(), "brave", registry)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized")
}

// TestConfigOldLoadCannotOverwriteSave holds a disabled configuration read
// while an enabled save publishes. Releasing and joining that old read leaves
// subsequent cached configuration enabled by the save.
func TestConfigOldLoadCannotOverwriteSave(t *testing.T) {
	repo := &configFixture{raw: `{"enabled":false,"providers":[]}`, entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, nil, nil, nil)
	done := make(chan error, 1)
	// Hold the load after it captured disabled settings, then save enabled
	// configuration so its late return exercises the generation comparison.
	go func() { _, err := service.GetWebSearchEmulationConfig(context.Background()); done <- err }()
	<-repo.entered
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture"}}}))
	close(repo.release)
	require.NoError(t, <-done)
	value, err := service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, value.Enabled)
}

// TestConfigCopiesAndWriteFailure mutates saved inputs, returned views, and
// optional integer pointers after publication. Cached values stay unchanged,
// and an injected failed replacement save leaves enablement present.
func TestConfigCopiesAndWriteFailure(t *testing.T) {
	repo := &configFixture{}
	service := NewConfigService(repo, nil, nil, nil)
	limit, sub, proxy, expiry := int64(10), int64(20), int64(30), int64(40)
	input := &WebSearchEmulationConfig{Enabled: true, Providers: []WebSearchProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture", QuotaLimit: &limit, SubscribedAt: &sub, ProxyID: &proxy, ExpiresAt: &expiry}}}
	require.NoError(t, service.SaveWebSearchEmulationConfig(context.Background(), input))
	// Mutate the caller's slice and all four original pointer values after saving;
	// cached copies should keep the values captured at publication.
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
	// Inject the write failure under the fixture lock before the next save,
	// isolating persistence rejection from prior successful publication.
	repo.mu.Lock()
	repo.fail = errors.New("write failed")
	repo.mu.Unlock()
	require.Error(t, service.SaveWebSearchEmulationConfig(context.Background(), &WebSearchEmulationConfig{}))
	current, err = service.GetWebSearchEmulationConfig(context.Background())
	require.NoError(t, err)
	require.True(t, current.Enabled)
}

// TestOldManagerBuildCannotOverwriteSavedGeneration holds startup proxy
// resolution while a save publishes newer credentials. The old build cannot
// replace that Manager; stopping then rejects another initialization attempt.
func TestOldManagerBuildCannotOverwriteSavedGeneration(t *testing.T) {
	repo := &configFixture{raw: `{"enabled":true,"providers":[{"type":"brave","api_key":"old","proxy_id":1}]}`}
	proxy := &configProxyFixture{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, proxy, func(c []ProviderConfig, g *WorkGroup) *Manager { return NewManager(c, nil, noSearchExecutor{}, g) }, nil)
	done := make(chan error, 1)
	// Pause initialization's first proxy lookup while the save builds without that
	// gate, exposing late startup publication against newer credentials.
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

// TestLoadedConfigurationThenSavedReplacement loads disabled stored settings
// before saving enabled replacement credentials. The cached configuration
// becomes enabled, and the current Manager contains the replacement supplier
// key.
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

// TestConsecutiveSavesKeepLastPublication holds the first save in proxy
// resolution while another save starts. After both calls finish, the published
// Manager contains the second save's credentials.
func TestConsecutiveSavesKeepLastPublication(t *testing.T) {
	repo := &configFixture{}
	proxies := &configProxyFixture{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewConfigService(repo, proxies, func(c []ProviderConfig, g *WorkGroup) *Manager { return NewManager(c, nil, noSearchExecutor{}, g) }, nil)
	id := int64(1)
	first := make(chan error, 1)
	second := make(chan error, 1)
	// Start the first save before launching its successor; the resolution gate
	// keeps the first call inside saveMu until the test releases it.
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

// searchQuotaFixture returns a new quota pointer for an optional configuration
// field.
func searchQuotaFixture(v int64) *int64 { return &v }

// GetValue snapshots the fixture's serialized setting and can hold its first
// read before returning. An empty snapshot returns settings.ErrSettingNotFound;
// contexts and key names are ignored.
//
// The value is copied under mu before the gate is claimed. A concurrent Set can
// therefore publish a newer value while this call still returns its older
// snapshot. entered identifies that captured read, and release permits it to
// finish; callers configure gates before concurrency and explicitly release the
// held call.
func (r *configFixture) GetValue(_ context.Context, _ string) (string, error) {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	// Gate only the first captured read so a save can perform its own credential
	// read while this older snapshot remains blocked.
	if r.entered != nil && r.firstRead.CompareAndSwap(false, true) {
		close(r.entered)
		<-r.release
	}
	if raw == "" {
		return "", settings.ErrSettingNotFound
	}
	return raw, nil
}

// Set replaces fixture raw under mu unless fail is configured. It ignores
// contexts and setting keys; a configured failure returns before mutation so
// publication tests can inspect the prior cached value.
func (r *configFixture) Set(_ context.Context, _, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.raw = value
	return nil
}

// URLs returns a fixed address for proxy ID one after optionally holding the
// first resolution. The fixture ignores supplied IDs and contexts, and callers
// provide entered and release channels before concurrent builds start.
//
// An atomic claim allows a later build to resolve immediately while the first
// remains blocked. That separation exposes a saved Manager overtaking startup
// construction. The test releases the initial build and joins its completion
// before checking which credentials remain installed.
func (p *configProxyFixture) URLs(context.Context, []int64) (map[int64]string, error) {
	// Let later resolutions complete while the first stays held, allowing the
	// publication test to install a newer generation before releasing old work.
	if p.first.CompareAndSwap(false, true) {
		close(p.entered)
		<-p.release
	}
	return map[int64]string{1: "http://fixture.invalid"}, nil
}
