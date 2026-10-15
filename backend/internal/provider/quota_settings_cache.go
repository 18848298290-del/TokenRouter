package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// quota_settings_cache.go caches OpenAI quota pause defaults and refreshes
// expired settings observations.
// OpenAI quota defaults live inside the wider Ops advanced-settings JSON, so
// selection reads can reuse a small threshold value without waiting for
// storage. Cold reads return zero thresholds and stale reads return their
// prior value while asynchronous refresh prepares the next observation.
// Synchronous warmup offers a completed read for callers that need
// initialized values.
//
// Refreshes use an independent five-second storage budget and publish one
// immutable cache entry. Missing settings cache zero thresholds normally;
// other read failures retain a prior value when available and shorten the
// retry interval. Apply can publish committed values or expire an existing
// observation. Forget removes future coalescing for its key, while an already
// running refresh can still publish after that application step.

// The settings key, cache lifetimes, database budget, and refresh key control
// how the OpenAI quota cache reads and republishes Ops thresholds.
const (
	// SettingKeyOpsAdvancedSettings identifies the shared Ops settings read by
	// the provider cache.
	SettingKeyOpsAdvancedSettings = "ops_advanced_settings"

	openAIQuotaAutoPauseSettingsCacheTTL  = 60 * time.Second // ordinary observation lifetime
	openAIQuotaAutoPauseSettingsErrorTTL  = 5 * time.Second  // storage failure retry delay
	openAIQuotaAutoPauseSettingsDBTimeout = 5 * time.Second  // detached storage read budget
	// openAIQuotaAutoPauseSettingsRefreshKey: coalesced refresh identity.
	openAIQuotaAutoPauseSettingsRefreshKey = "openai_quota_auto_pause_settings"
)

// QuotaSettingsCache retains an Ops settings reader, decoder, and
// process-local threshold observation. Its asynchronous reads outlive Get
// calls, so dependencies remain usable until those refreshes complete. Keep
// this cache at its original address after first use, as required by its
// atomic cache and flight synchronization.
type QuotaSettingsCache struct {
	// settingRepo remains borrowed by asynchronous refreshes after Get returns.
	// Keep the reader and its storage dependencies usable until those calls
	// finish.
	settingRepo interface {
		// GetValue reads the Ops JSON for the supplied settings key. Missing-key
		// errors match notFound; other errors select the short retry lifetime and
		// retain available prior thresholds.
		GetValue(context.Context, string) (string, error)
	} // retained Ops settings reader
	notFound error // repository missing-key sentinel
	// decode interprets successful reads, including malformed or partial Ops JSON.
	// Warmup and detached refreshes can invoke it concurrently, so keep its
	// captured state usable and synchronized until those reads finish.
	decode func(string) QuotaAutoPauseSettings
	// openAIQuotaAutoPauseSettingsCache publishes immutable entry pointers,
	// while openAIQuotaAutoPauseSettingsSF joins asynchronous reads for one key.
	// Set and Apply can publish alongside existing reads, so the last Store
	// determines which observation is visible.
	// openAIQuotaAutoPauseSettingsCache: atomically published threshold
	// observation
	openAIQuotaAutoPauseSettingsCache atomic.Value
	// openAIQuotaAutoPauseSettingsSF: coalesced asynchronous refresh group
	openAIQuotaAutoPauseSettingsSF singleflight.Group
}

// cachedOpenAIQuotaAutoPauseSettings holds a published scalar threshold value
// and its wall-clock expiration.
type cachedOpenAIQuotaAutoPauseSettings struct {
	settings  QuotaAutoPauseSettings // cached window threshold ratios
	expiresAt int64                  // Unix nanoseconds
}

// NewQuotaSettingsCache stores dependencies for asynchronous reads and
// synchronous warmup. A nil repository leaves refresh inactive; supply a
// decoder when storage can return a value and a notFound sentinel matching
// the repository's missing-key error.
func NewQuotaSettingsCache(repo interface {
	// GetValue reads the Ops JSON used by background and warmup refreshes. Its
	// missing-key error should match notFound so absent settings use the normal
	// observation lifetime.
	GetValue(context.Context, string) (string, error)
}, notFound error, decode func(string) QuotaAutoPauseSettings,
) *QuotaSettingsCache {
	return &QuotaSettingsCache{settingRepo: repo, notFound: notFound, decode: decode}
}

// GetOpenAIQuotaAutoPauseSettings returns a fresh or previously cached
// threshold value immediately. Nil receivers and cold caches return zero
// defaults; missing or expired entries start a coalesced background refresh,
// and ctx is unused. Retain the repository and decoder until that detached
// read finishes.
func (s *QuotaSettingsCache) GetOpenAIQuotaAutoPauseSettings(ctx context.Context) QuotaAutoPauseSettings {
	if s == nil {
		return QuotaAutoPauseSettings{}
	}
	cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings)
	now := time.Now().UnixNano()
	if cached != nil && now < cached.expiresAt {
		return cached.settings
	}
	// DoChan owns the asynchronous execution and buffers its result, so this
	// reader can return immediately. Refresh publishes through the atomic cache
	// and callers do not need to receive from the unused result channel.
	s.openAIQuotaAutoPauseSettingsSF.DoChan(openAIQuotaAutoPauseSettingsRefreshKey, func() (any, error) {
		s.refreshOpenAIQuotaAutoPauseSettings(context.Background())
		return nil, nil
	})
	if cached != nil {
		return cached.settings
	}
	return QuotaAutoPauseSettings{}
}

// WarmOpenAIQuotaAutoPauseSettings refreshes synchronously and returns the
// cache value visible after that call. Nil receivers return zero
// defaults; the refresh retains context values but removes caller
// cancellation and applies its own five-second timeout. Supply a nonnil
// context when storage is configured.
func (s *QuotaSettingsCache) WarmOpenAIQuotaAutoPauseSettings(ctx context.Context) QuotaAutoPauseSettings {
	if s == nil {
		return QuotaAutoPauseSettings{}
	}
	s.refreshOpenAIQuotaAutoPauseSettings(ctx)
	cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings)
	if cached == nil {
		return QuotaAutoPauseSettings{}
	}
	return cached.settings
}

// refreshOpenAIQuotaAutoPauseSettings reads and publishes one decoded Ops
// threshold observation. A nil receiver or repository skips the operation; a
// usable decoder handles successful storage values.
//
// The storage call retains context values while receiving a separate
// five-second timeout, allowing publication after the initiating caller
// cancels. Missing keys select zero defaults with normal lifetime. Other
// errors retain the value visible at failure time when available and use the
// shorter error lifetime. Expiration is measured after the read before one
// atomic Store publishes both fields.
func (s *QuotaSettingsCache) refreshOpenAIQuotaAutoPauseSettings(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	// Detach the initiating cancellation while retaining context values; one
	// caller's lifetime then cannot abort a shared threshold refresh.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIQuotaAutoPauseSettingsDBTimeout)
	defer cancel()

	settings := QuotaAutoPauseSettings{}
	ttl := openAIQuotaAutoPauseSettingsCacheTTL
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpsAdvancedSettings)
	if err == nil {
		settings = s.decode(raw)
	} else if !errors.Is(err, s.notFound) {
		// A failed read retains previously usable ratios when available. Shorten
		// their lifetime so a later selection retries the failed lookup.
		if prior, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); prior != nil {
			settings = prior.settings
		}
		ttl = openAIQuotaAutoPauseSettingsErrorTTL
	}

	// Start this observation's lifetime after the storage call completes; its
	// value and retry timing then become visible through one entry pointer.
	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

// SetOpenAIQuotaAutoPauseSettings publishes a value copy with a normal cache
// lifetime, ignoring nil receivers. Each ratio passes through quotaClamp,
// which bounds ordered values and retains NaN; the method performs no
// persistence.
func (s *QuotaSettingsCache) SetOpenAIQuotaAutoPauseSettings(settings QuotaAutoPauseSettings) {
	if s == nil {
		return
	}
	settings.DefaultThreshold5h = quotaClamp(settings.DefaultThreshold5h)
	settings.DefaultThreshold7d = quotaClamp(settings.DefaultThreshold7d)
	// Publish a value copy and lifetime through one entry pointer so each
	// concurrent reader captures a complete observation with one atomic load.
	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(openAIQuotaAutoPauseSettingsCacheTTL).UnixNano(),
	})
}

// Apply publishes explicitly supplied committed thresholds or expires an
// existing cache observation for a later read. Call it on a nonnil cache
// after persistence; absent prior data stays cold when explicit is false.
//
// Forgetting the refresh key lets subsequent reads start independent work.
// Existing refresh closures continue and can Store their own values after
// this call, so application does not establish ordering against those earlier
// reads. Expiring a prior entry retains its thresholds for the next stale
// read while requesting a replacement.
//
// Composite runtime readback leaves OpenAIQuotaAutoPauseSettingsSet false, so
// that consumer uses the expiry path even after a submitted quota update.
// A stale read can therefore return prior ratios while its detached refresh
// loads the saved Ops JSON.
func (s *QuotaSettingsCache) Apply(value QuotaAutoPauseSettings, explicit bool) {
	// Remove this key from the flight lookup before applying cache data. Existing
	// closures keep their cache receiver and reach Store independently of that
	// lookup.
	s.openAIQuotaAutoPauseSettingsSF.Forget(openAIQuotaAutoPauseSettingsRefreshKey)
	if explicit {
		s.SetOpenAIQuotaAutoPauseSettings(value)
	} else if cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); cached != nil {
		// Readers that observe this expired entry retain its prior ratios while
		// requesting a replacement from persisted Ops JSON through the refresh
		// group.
		s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
			settings:  cached.settings,
			expiresAt: 0,
		})
	}
}
