package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Search settings contain secrets and optional quota dates, while the running
// Manager retains a copied execution configuration. Cache reads, administrator
// views, and published candidates receive separate slices and integer pointers
// so callers can edit their own values without mutating stored in-process
// snapshots.
//
// Successful saves write the setting before advancing a process-local
// generation and publishing a replacement cache and Manager. A load or Manager
// build captures its starting generation and cannot publish over a later save.
// Cache misses share a synchronous background-budgeted load; database errors
// cache an empty value briefly, and later cache hits return that value with nil
// error. Configurations and searches share shutdown admission through one
// WorkGroup.

// These values identify the search setting and shared load, bound configuration
// caching and database reads, and limit administrator search inputs.
const (
	SettingKeyWebSearchEmulationConfig = "web_search_emulation_config" // persisted search configuration key
	webSearchEmulationCacheTTL         = 60 * time.Second              // successful cached configuration lifetime
	webSearchEmulationErrorTTL         = 5 * time.Second               // failed-read cached empty-value lifetime
	webSearchEmulationDBTimeout        = 5 * time.Second               // background database read budget
	sfKeyWebSearchConfig               = "web_search_emulation_config" // shared configuration load key
	maxWebSearchProviders              = 10                            // maximum configured provider entry count

	testSearchTimeout = 15 * time.Second // administrator test search budget
)

// validProviderTypes enumerates the literal supplier identifiers accepted by
// ValidateConfig before configuration persistence and Manager publication.
var validProviderTypes = map[string]bool{ProviderTypeBrave: true, ProviderTypeTavily: true}

// ConfigRepository reads and replaces serialized search settings in persistent
// storage.
type ConfigRepository interface {
	// GetValue reads the serialized setting using ctx. load interprets
	// settings.ErrSettingNotFound as empty successful configuration and other
	// errors as short-lived empty cache results; credential merging separately
	// suppresses repository errors.
	GetValue(context.Context, string) (string, error)
	// Set replaces the serialized setting using the save context and returns its
	// persistence result. Save publishes that call's input after success; returned
	// errors prevent its own generation advance and publication.
	Set(context.Context, string, string) error
}

// ProxyResolver maps configured search proxy IDs to their stored execution
// addresses.
type ProxyResolver interface {
	// URLs resolves the supplied positive proxy IDs to addresses using ctx.
	// Missing map entries skip their configured suppliers; publication logs an
	// error and continues with any returned entries.
	URLs(context.Context, []int64) (map[int64]string, error)
}
type (
	// ManagerFactory builds a Manager from copied supplier settings using the
	// shared WorkGroup. Construction runs outside publishMu but during Save's
	// saveMu hold, so callbacks must not synchronously reenter Save.
	ManagerFactory func([]ProviderConfig, *WorkGroup) *Manager
	// cachedConfig pairs an immutable configuration snapshot with its cache
	// expiration time.
	cachedConfig struct {
		// config and expiresAt are published together through one atomic pointer.
		// Published config remains immutable, and readers copy it before returning
		// values to callers.
		config    *WebSearchEmulationConfig // cached configuration snapshot pointer
		expiresAt time.Time                 // copied snapshot expiration time
	}
)

// ConfigService loads, saves, and publishes search configuration and Manager
// generations.
type ConfigService struct {
	repo ConfigRepository // serialized search configuration storage
	// proxies resolves positive configured IDs during Manager construction. A nil
	// resolver leaves supplied proxy IDs without map entries and skips those
	// suppliers.
	proxies ProxyResolver
	// factory constructs enabled Managers with the shared work tracker. Nil
	// disables Manager creation while configuration saves and cache publication
	// still proceed.
	factory ManagerFactory
	// registry is shared with gateway and administrator execution.
	// NewConfigService uses the supplied instance or creates one when the argument
	// is nil.
	registry *Registry
	// work counts cache misses, saves, initialization, and Manager operations
	// across generations. Unexpired cache hits return before registration and
	// remain readable after stop.
	work *WorkGroup
	// cache publishes immutable configuration and expiration pairs. load and Save
	// coordinate their stores with revision under publishMu, while hits read
	// atomically and clone the value.
	cache atomic.Pointer[cachedConfig]
	// sf merges overlapping cache misses. Save forgets its key so a future miss
	// can begin another load while an older load is still returning.
	sf singleflight.Group
	// publishMu protects revision comparisons and generation-consistent cache or
	// Manager publication. Resolver and factory callbacks finish before Manager
	// publication acquires this lock.
	publishMu sync.Mutex
	// saveMu serializes saves through credential merge, persistence, and Manager
	// construction. Callbacks invoked during those phases must return without
	// synchronously saving through this service.
	saveMu sync.Mutex
	// revision advances after successful persistence in this process. Earlier
	// loads and Manager builds compare it under publishMu before publishing their
	// results.
	revision uint64
}

// Registry publishes the current Manager pointer and retires replaced execution
// instances.
type Registry struct {
	// current is the shared Manager pointer read by gateway and administrator
	// callers. Set atomically replaces it; previously returned Managers remain
	// usable until WorkGroup stops.
	current atomic.Pointer[Manager]
}

// WebSearchEmulationConfig stores the global enable switch and ordered supplier
// settings.
type WebSearchEmulationConfig struct {
	// Enabled requests search emulation. Manager construction additionally needs
	// at least one configured provider and a factory; gateway enabled checks also
	// require a nonempty list.
	Enabled bool `json:"enabled"`
	// Providers holds supplier settings in administrator test order. CloneConfig
	// copies the slice and all optional integer pointers before retaining or
	// returning it.
	Providers []WebSearchProviderConfig `json:"providers"`
}

// WebSearchProviderConfig stores one supplier's credentials, quota
// configuration, and proxy reference.
type WebSearchProviderConfig struct {
	Type string `json:"type"` // literal configured supplier identifier
	// APIKey is the supplier secret. Save merges an exactly empty value from
	// persisted settings by Type; enabled entries need a key after merging.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyConfigured reports whether the supplied APIKey is nonempty in
	// decorated views. PopulateWebSearchUsage and SanitizeWebSearchConfig
	// recompute it before returning their copies.
	APIKeyConfigured bool `json:"api_key_configured"`
	// QuotaLimit sets the Manager's call limit. Nil or zero becomes unlimited, and
	// ValidateConfig rejects negative values before Save persists them.
	QuotaLimit *int64 `json:"quota_limit"`
	// SubscribedAt is the Unix-second subscription anchor copied into Manager
	// settings. Nil or zero selects the default counter lifetime; configured dates
	// use UTC monthly clamping.
	SubscribedAt *int64 `json:"subscribed_at,omitempty"`
	// QuotaUsed is the display counter. Decoration replaces it with the current
	// Manager's read result when available, suppressing read errors; an absent
	// Manager retains the supplied value.
	QuotaUsed int64 `json:"quota_used,omitempty"`
	// ProxyID selects a stored supplier proxy. Nil selects no configured proxy;
	// any supplied pointer requires a returned resolver map entry before its
	// supplier can be published.
	ProxyID *int64 `json:"proxy_id"`
	// ExpiresAt is an optional Unix-second expiration copied into Manager
	// settings. A keyed supplier remains eligible until the current Unix second
	// exceeds this value.
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

// WebSearchTestResult returns the successful administrator-test supplier,
// results, and submitted query.
type WebSearchTestResult struct {
	Provider string         `json:"provider"` // successful configured supplier type
	Results  []SearchResult `json:"results"`  // normalized administrator test results
	Query    string         `json:"query"`    // query submitted by test
}

// NewConfigService binds persistent settings, optional proxy resolution, and
// Manager construction to one runtime. Nil registry creates a shared registry;
// nil factory leaves Manager publication empty, and construction starts no
// operations.
func NewConfigService(repo ConfigRepository, proxies ProxyResolver, factory ManagerFactory, registry *Registry) *ConfigService {
	if registry == nil {
		registry = NewRegistry()
	}
	return &ConfigService{repo: repo, proxies: proxies, factory: factory, registry: registry, work: NewWorkGroup()}
}

// GetWebSearchEmulationConfig returns a configuration copy from an unexpired
// cache or a shared load. A cache hit bypasses shutdown admission and returns
// nil error, including an empty value cached after a previous read failure.
// Cache misses join WorkGroup and return a nonnil empty configuration with
// their load or admission error. ctx does not control synchronous singleflight
// waiting or the background database budget.
func (s *ConfigService) GetWebSearchEmulationConfig(ctx context.Context) (*WebSearchEmulationConfig, error) {
	if value := s.cache.Load(); value != nil && time.Now().Before(value.expiresAt) {
		return CloneConfig(value.config), nil
	}
	done, err := s.work.Begin()
	if err != nil {
		return &WebSearchEmulationConfig{}, err
	}
	defer done()
	// Join synchronous cache-miss work using the service's background load budget;
	// a caller's request cancellation does not release this singleflight wait.
	result, err, _ := s.sf.Do(sfKeyWebSearchConfig, func() (any, error) { return s.load() })
	if err != nil {
		return &WebSearchEmulationConfig{}, err
	}
	// Map an unexpected shared-load value to empty settings so configuration
	// callers still receive the method's nonnil result shape.
	cfg, _ := result.(*WebSearchEmulationConfig)
	if cfg == nil {
		return &WebSearchEmulationConfig{}, nil
	}
	return CloneConfig(cfg), nil
}

// load reads configuration within a background five-second budget and selects
// its cache lifetime. Missing settings become an empty successful value; other
// repository errors cache an empty value for five seconds and remain errors for
// this load's callers.
//
// The method snapshots revision before reading, then compares it under
// publishMu before storing. A later successful save wins even when this read
// returns afterward: load returns the newer cached copy and suppresses its own
// stale error. The cached snapshot is separately cloned so the returned value
// can be changed independently.
func (s *ConfigService) load() (*WebSearchEmulationConfig, error) {
	s.publishMu.Lock()
	revision := s.revision
	s.publishMu.Unlock()
	// Give the shared repository load its own budget so cancellation of one
	// request does not cancel configuration work awaited by others.
	ctx, cancel := context.WithTimeout(context.Background(), webSearchEmulationDBTimeout)
	defer cancel()
	raw, err := s.repo.GetValue(ctx, SettingKeyWebSearchEmulationConfig)
	ttl := webSearchEmulationCacheTTL
	cfg := &WebSearchEmulationConfig{}
	if err == nil {
		cfg = ParseConfig(raw)
	} else if errors.Is(err, settings.ErrSettingNotFound) {
		// Treat an absent stored setting as disabled configuration; it receives the
		// normal cache lifetime and no load error.
		err = nil
	} else {
		// Cache the empty failed-read value briefly while returning this load's
		// error; subsequent cache hits return that value without the error.
		ttl = webSearchEmulationErrorTTL
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	// A save advanced generation during the read; prefer its published cache so
	// late database results cannot undo the newer local save.
	if revision != s.revision {
		if current := s.cache.Load(); current != nil {
			return CloneConfig(current.config), nil
		}
		return CloneConfig(cfg), err
	}
	// Publish this load after the generation comparison under publishMu, keeping
	// a concurrent save from intervening between validation and cache replacement.
	s.cache.Store(&cachedConfig{config: CloneConfig(cfg), expiresAt: time.Now().Add(ttl)})
	return cfg, err
}

// SaveWebSearchEmulationConfig persists and publishes a copied, validated
// nonnil configuration. The method serializes saves through Manager
// construction, fills exactly empty keys from a direct persistent read, and
// requires enabled suppliers to have a key afterward. Credential-read errors
// are suppressed, so missing-key validation can still reject the save.
//
// Persistence succeeds before revision advances and the copied cache replaces
// its prior value. Forget releases future cache misses from an older shared
// load; generation comparison prevents that load or an older Manager build from
// publishing over this save. Resolver failures can skip configured suppliers
// after persistence, and the factory callback supplies no returned construction
// error.
// @project-doc docs/interfaces/configuration.md#search_configuration
func (s *ConfigService) SaveWebSearchEmulationConfig(ctx context.Context, input *WebSearchEmulationConfig) error {
	done, err := s.work.Begin()
	if err != nil {
		return err
	}
	defer done()
	// Serialize credential merging and persistence through Manager construction so
	// consecutive local saves publish in their protected write order.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	cfg := CloneConfig(input)
	if err := ValidateConfig(cfg); err != nil {
		return apperror.BadRequest("INVALID_WEB_SEARCH_CONFIG", err.Error())
	}
	s.mergeExistingAPIKeys(ctx, cfg)
	if cfg.Enabled {
		for _, p := range cfg.Providers {
			if p.APIKey == "" {
				return apperror.BadRequest("MISSING_API_KEY", fmt.Sprintf("provider %s has no API key configured", p.Type))
			}
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("websearch: marshal config: %w", err)
	}
	// Persist the merged configuration before exposing its cache or Manager;
	// repository failure returns before this save advances its generation or replaces
	// the cached value.
	if err := s.repo.Set(ctx, SettingKeyWebSearchEmulationConfig, string(data)); err != nil {
		return fmt.Errorf("websearch: save config: %w", err)
	}
	s.publishMu.Lock()
	// Advance generation with cache replacement and forget older load sharing so
	// future misses cannot join work that predates this save.
	s.revision++
	revision := s.revision
	s.sf.Forget(sfKeyWebSearchConfig)
	s.cache.Store(&cachedConfig{config: CloneConfig(cfg), expiresAt: time.Now().Add(webSearchEmulationCacheTTL)})
	s.publishMu.Unlock()
	s.publishManager(ctx, cfg, revision)
	return nil
}

// Initialize builds a Manager from the service's cached configuration and
// participates in shutdown admission. It suppresses the configuration-read
// result, then copies any cached value with its generation under publishMu; a
// read failure can therefore initialize with the cached empty value.
//
// Resolver and factory work runs after that snapshot lock is released.
// publishManager compares the captured generation before installation, so a
// slow initialization build yields to a later successful save. Admission
// failure is returned, while read or resolver failures are handled by the
// selected cache and publication paths.
func (s *ConfigService) Initialize(ctx context.Context) error {
	done, err := s.work.Begin()
	if err != nil {
		return err
	}
	defer done()
	// Continue initialization using the cached value after a read failure; search
	// startup can proceed with empty or previously saved configuration.
	_, _ = s.GetWebSearchEmulationConfig(ctx)
	s.publishMu.Lock()
	revision := s.revision
	var cfg *WebSearchEmulationConfig
	if value := s.cache.Load(); value != nil {
		cfg = CloneConfig(value.config)
	}
	s.publishMu.Unlock()
	s.publishManager(ctx, cfg, revision)
	return nil
}

// publishManager builds eligible supplier settings and installs their Manager
// when revision still matches. Disabled, absent, empty, or factory-less
// configurations publish nil for that generation. Keyless suppliers are
// skipped, positive proxy IDs are resolved together, and every supplied proxy
// pointer needs an entry in the returned address map.
//
// Resolution and construction run before taking publishMu so a slow
// initialization build can overlap a save. Resolver errors are logged while
// returned entries remain usable; missing entries skip their suppliers. A stale
// constructed Manager is retired instead of installed. Current-generation
// installation atomically replaces the registry pointer and retires its
// predecessor.
func (s *ConfigService) publishManager(ctx context.Context, cfg *WebSearchEmulationConfig, revision uint64) {
	var manager *Manager
	if cfg != nil && cfg.Enabled && len(cfg.Providers) > 0 && s.factory != nil {
		var ids []int64
		for _, p := range cfg.Providers {
			if p.ProxyID != nil && *p.ProxyID > 0 {
				ids = append(ids, *p.ProxyID)
			}
		}
		var urls map[int64]string
		if len(ids) > 0 && s.proxies != nil {
			var err error
			urls, err = s.proxies.URLs(ctx, ids)
			if err != nil {
				// Keep any resolver entries already returned and skip absent IDs later, so
				// one resolution failure need not discard directly configured suppliers.
				slog.Warn("websearch: failed to resolve proxy URLs", "error", err)
			}
		}
		configs := make([]ProviderConfig, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if p.APIKey == "" {
				continue
			}
			pc := ProviderConfig{Type: p.Type, APIKey: p.APIKey, SubscribedAt: CloneInt64(p.SubscribedAt), ExpiresAt: CloneInt64(p.ExpiresAt)}
			if p.QuotaLimit != nil {
				pc.QuotaLimit = *p.QuotaLimit
			}
			if p.ProxyID != nil {
				pc.ProxyID = *p.ProxyID
				url, ok := urls[*p.ProxyID]
				if !ok {
					// Skip a supplier with an unresolved configured proxy instead of silently
					// selecting the executor's empty direct-connection proxy key.
					slog.Warn("websearch: proxy not found for provider, skipping", "provider", p.Type, "proxy_id", *p.ProxyID)
					continue
				}
				pc.ProxyURL = url
			}
			configs = append(configs, pc)
		}
		manager = s.factory(configs, s.work)
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	// Retire a completed build whose configuration generation lost to a save; only
	// the current generation may replace the shared registry.
	if revision != s.revision {
		if manager != nil {
			manager.Retire()
		}
		return
	}
	s.registry.Set(manager)
}

// IsWebSearchEmulationEnabled requires a successful configuration read,
// Enabled, and a nonempty provider list. It reports false for returned read
// errors; supplier credentials, proxy eligibility, and the published Manager
// are checked elsewhere.
func (s *ConfigService) IsWebSearchEmulationEnabled(ctx context.Context) bool {
	cfg, err := s.GetWebSearchEmulationConfig(ctx)
	// A returned configuration error disables gateway emulation for this check;
	// valid settings still need an enabled nonempty provider list.
	return err == nil && cfg.Enabled && len(cfg.Providers) > 0
}

// StopContext closes shared operation admission and waits using ctx before
// retiring the current Manager. A wait error returns immediately with clients
// still available to unfinished operations; later calls can wait again.
func (s *ConfigService) StopContext(ctx context.Context) error {
	err := s.work.Stop(ctx)
	// Retire clients after shared work drains; a timeout leaves them open for
	// searches and quota cleanup that may still be executing.
	if err == nil {
		if m := s.registry.Get(); m != nil {
			m.Retire()
		}
	}
	return err
}

// Registry returns the shared Manager registry bound to this configuration
// service. Gateway and administrator consumers read its current pointer;
// returned Managers keep their own copied settings across replacements.
func (s *ConfigService) Registry() *Registry { return s.registry }

// NewRegistry constructs an empty registry ready for atomic Manager pointer
// publication.
func NewRegistry() *Registry { return &Registry{} }

// Get returns the currently published Manager pointer, or nil for an empty
// registry. The pointer is shared, and replacement leaves existing holders
// responsible for their own request completion.
func (r *Registry) Get() *Manager { return r.current.Load() }

// Set atomically publishes m, including nil, and retires a different previous
// Manager after the swap. Existing pointer holders can continue their searches,
// whose shared WorkGroup counts work until those calls and cleanup finish.
//
// Publication precedes the old executor's idle close, so new readers can select
// the replacement while previous exchanges continue. Reinstalling the same
// pointer skips retirement for that swap. Executor callbacks run synchronously
// during retirement and need to return for this setter to finish.
func (r *Registry) Set(m *Manager) {
	// Publish the replacement before closing the predecessor's idle connections so
	// existing exchanges retain their clients while new readers obtain m.
	previous := r.current.Swap(m)
	if previous != nil && previous != m {
		previous.Retire()
	}
}

// CloneConfig copies configuration, its provider slice, and all four optional
// integer pointers. Nil input returns nil; nonnil callers can change the copied
// slice, fields, and pointed-to values independently.
func CloneConfig(in *WebSearchEmulationConfig) *WebSearchEmulationConfig {
	if in == nil {
		return nil
	}
	out := *in
	if in.Providers != nil {
		out.Providers = make([]WebSearchProviderConfig, len(in.Providers))
		copy(out.Providers, in.Providers)
		for i := range out.Providers {
			p := &out.Providers[i]
			p.QuotaLimit = CloneInt64(p.QuotaLimit)
			p.SubscribedAt = CloneInt64(p.SubscribedAt)
			p.ProxyID = CloneInt64(p.ProxyID)
			p.ExpiresAt = CloneInt64(p.ExpiresAt)
		}
	}
	return &out
}

// ValidateConfig accepts nil and checks provider count, literal supported
// types, nonnegative quota values, and duplicate types. It accepts empty
// provider lists and zero quotas; Save separately checks enabled entries after
// merging credentials.
func ValidateConfig(cfg *WebSearchEmulationConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Providers) > maxWebSearchProviders {
		return fmt.Errorf("too many providers (max %d)", maxWebSearchProviders)
	}
	seen := make(map[string]bool, len(cfg.Providers))
	for i, p := range cfg.Providers {
		if !validProviderTypes[p.Type] {
			return fmt.Errorf("provider[%d]: invalid type %q", i, p.Type)
		}
		if p.QuotaLimit != nil && *p.QuotaLimit < 0 {
			return fmt.Errorf("provider[%d]: quota_limit must be > 0 or null", i)
		}
		if seen[p.Type] {
			return fmt.Errorf("provider[%d]: duplicate type %q", i, p.Type)
		}
		seen[p.Type] = true
	}
	return nil
}

// ParseConfig decodes stored JSON into a nonnil configuration without provider
// validation. Empty, null, or malformed input yields empty settings, with
// malformed input logged; unknown JSON fields are ignored by decoding.
func ParseConfig(raw string) *WebSearchEmulationConfig {
	cfg := &WebSearchEmulationConfig{}
	if raw == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		// Discard partially decoded malformed settings as empty configuration so
		// later publication cannot execute a partially recovered supplier list.
		slog.Warn("websearch: failed to parse config JSON", "error", err)
		return &WebSearchEmulationConfig{}
	}
	return cfg
}

// mergeExistingAPIKeys fills exactly empty incoming keys from persistent
// configuration by literal supplier type. It mutates cfg, leaves nonempty
// incoming keys intact, and treats failed existing reads as unavailable
// credentials. Nil cfg or missing existing configuration leaves incoming values
// unchanged.
//
// Type matching keeps a supplier's omitted secret with that same supplier when
// administrator entries are reordered. The lookup retains the last nonempty
// existing key for a duplicate stored type; validation applies to incoming
// entries separately. Save holds saveMu while this direct repository read and
// merge complete.
func (s *ConfigService) mergeExistingAPIKeys(ctx context.Context, cfg *WebSearchEmulationConfig) {
	// Use persistent credentials rather than a possibly stale cache; failed reads
	// leave incoming empty keys for Save's enabled-entry validation to reject.
	existing, _ := s.getWebSearchEmulationConfigRaw(ctx)
	if existing == nil || cfg == nil {
		return
	}
	existingByType := make(map[string]string, len(existing.Providers))
	for _, p := range existing.Providers {
		if p.APIKey != "" {
			existingByType[p.Type] = p.APIKey
		}
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].APIKey == "" {
			if key, ok := existingByType[cfg.Providers[i].Type]; ok {
				cfg.Providers[i].APIKey = key
			}
		}
	}
}

// getWebSearchEmulationConfigRaw reads the stored JSON using the supplied
// context and bypasses the cache. Repository errors propagate; successful
// malformed JSON decodes through ParseConfig into empty settings with nil
// error.
func (s *ConfigService) getWebSearchEmulationConfigRaw(ctx context.Context) (*WebSearchEmulationConfig, error) {
	raw, err := s.repo.GetValue(ctx, SettingKeyWebSearchEmulationConfig)
	if err != nil {
		return nil, err
	}
	return ParseConfig(raw), nil
}

// PopulateWebSearchUsage returns a copied configuration with key-presence flags
// and current Manager counters. Nil configuration returns nil; a nonnil
// configuration requires a nonnil registry. API keys remain in the copy, so
// administrator settings use this view for credential-bearing responses.
//
// The registry is sampled once, while usage is read separately for each
// supplier type. An absent Manager retains input QuotaUsed; a present Manager
// replaces it with the returned count even when its read error is suppressed.
// Copied pointer fields remain independent, and usage can describe another
// published generation than the supplied configuration.
func PopulateWebSearchUsage(ctx context.Context, cfg *WebSearchEmulationConfig, registry *Registry) *WebSearchEmulationConfig {
	if cfg == nil {
		return nil
	}
	out := *CloneConfig(cfg)
	out.Providers = make([]WebSearchProviderConfig, len(cfg.Providers))

	mgr := registry.Get()

	for i, p := range cfg.Providers {
		out.Providers[i] = CloneConfig(&WebSearchEmulationConfig{Providers: []WebSearchProviderConfig{p}}).Providers[0]
		out.Providers[i].APIKeyConfigured = p.APIKey != ""

		// Retain supplied usage when no Manager is published; otherwise accept its
		// returned count and suppress display-only storage errors.
		if mgr != nil {
			used, _ := mgr.GetUsage(ctx, p.Type)
			out.Providers[i].QuotaUsed = used
		}
	}
	return &out
}

// SanitizeWebSearchConfig returns a copied configuration with API keys cleared
// and presence flags derived from the input secrets. Nil input returns nil;
// other calls require a nonnil registry. Provider slices and optional integer
// pointers are copied before decoration.
//
// The current Manager is sampled once and queried per supplier, so usage can
// change between supplier reads or belong to a different publication
// generation. A missing Manager retains input usage. With a Manager, the
// returned count replaces usage even on a suppressed read error; clearing a key
// keeps its independently derived presence flag.
func SanitizeWebSearchConfig(ctx context.Context, cfg *WebSearchEmulationConfig, registry *Registry) *WebSearchEmulationConfig {
	if cfg == nil {
		return nil
	}
	out := *CloneConfig(cfg)
	out.Providers = make([]WebSearchProviderConfig, len(cfg.Providers))

	mgr := registry.Get()

	for i, p := range cfg.Providers {
		out.Providers[i] = CloneConfig(&WebSearchEmulationConfig{Providers: []WebSearchProviderConfig{p}}).Providers[0]
		out.Providers[i].APIKeyConfigured = p.APIKey != ""
		// Clear each copied secret after computing presence so sanitized callers can
		// show configured credentials while receiving no key value.
		out.Providers[i].APIKey = ""

		// Retain supplied usage when no Manager is published; otherwise accept its
		// returned count and suppress display-only storage errors.
		if mgr != nil {
			used, _ := mgr.GetUsage(ctx, p.Type)
			out.Providers[i].QuotaUsed = used
		}
	}
	return &out
}

// ResetWebSearchUsage delegates the literal supplier counter reset through a
// nonnil registry. An empty registry returns a Manager-uninitialized error;
// otherwise Manager applies its configured quota store or treats absent storage
// as success.
func ResetWebSearchUsage(ctx context.Context, providerType string, registry *Registry) error {
	mgr := registry.Get()
	if mgr == nil {
		return fmt.Errorf("web search manager not initialized")
	}
	return mgr.ResetUsage(ctx, providerType)
}

// TestWebSearch runs the current Manager's administrator search with a
// fifteen-second child budget and default result count. A nonnil registry is
// required; an empty registry returns an initialization error before any
// supplier attempt.
func TestWebSearch(ctx context.Context, query string, registry *Registry) (*WebSearchTestResult, error) {
	mgr := registry.Get()
	if mgr == nil {
		return nil, fmt.Errorf("web search: manager not initialized, save config first")
	}
	testCtx, cancel := context.WithTimeout(ctx, testSearchTimeout)
	defer cancel()
	resp, providerName, err := mgr.TestSearch(testCtx, SearchRequest{
		Query:      query,
		MaxResults: defaultMaxResults,
	})
	if err != nil {
		return nil, err
	}
	return &WebSearchTestResult{
		Provider: providerName,
		Results:  resp.Results,
		Query:    resp.Query,
	}, nil
}
