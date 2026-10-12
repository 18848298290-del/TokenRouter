package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// SettingKeyOpsAdvancedSettings identifies the shared Ops settings read by the provider cache.
	SettingKeyOpsAdvancedSettings = "ops_advanced_settings"

	openAIQuotaAutoPauseSettingsCacheTTL   = 60 * time.Second
	openAIQuotaAutoPauseSettingsErrorTTL   = 5 * time.Second
	openAIQuotaAutoPauseSettingsDBTimeout  = 5 * time.Second
	openAIQuotaAutoPauseSettingsRefreshKey = "openai_quota_auto_pause_settings"
)

// QuotaSettingsCache caches provider thresholds decoded from the shared Ops settings JSON.
type QuotaSettingsCache struct {
	settingRepo interface {
		GetValue(context.Context, string) (string, error)
	}
	notFound                          error
	decode                            func(string) QuotaAutoPauseSettings
	openAIQuotaAutoPauseSettingsCache atomic.Value
	openAIQuotaAutoPauseSettingsSF    singleflight.Group
}

type cachedOpenAIQuotaAutoPauseSettings struct {
	settings  QuotaAutoPauseSettings
	expiresAt int64
}

// NewQuotaSettingsCache creates a cache supporting synchronous warmup and asynchronous refresh on stale reads.
func NewQuotaSettingsCache(repo interface {
	GetValue(context.Context, string) (string, error)
}, notFound error, decode func(string) QuotaAutoPauseSettings,
) *QuotaSettingsCache {
	return &QuotaSettingsCache{settingRepo: repo, notFound: notFound, decode: decode}
}

func (s *QuotaSettingsCache) GetOpenAIQuotaAutoPauseSettings(ctx context.Context) QuotaAutoPauseSettings {
	if s == nil {
		return QuotaAutoPauseSettings{}
	}
	cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings)
	now := time.Now().UnixNano()
	if cached != nil && now < cached.expiresAt {
		return cached.settings
	}
	// Refresh missing or expired settings in the background so reads do not wait for the database.
	// singleflight coalesces refreshes. The result channel is unused because
	// the refresh publishes its result through the atomic cache.
	s.openAIQuotaAutoPauseSettingsSF.DoChan(openAIQuotaAutoPauseSettingsRefreshKey, func() (any, error) {
		s.refreshOpenAIQuotaAutoPauseSettings(context.Background())
		return nil, nil
	})
	if cached != nil {
		return cached.settings // Return the prior value while refreshing.
	}
	return QuotaAutoPauseSettings{}
}

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

func (s *QuotaSettingsCache) refreshOpenAIQuotaAutoPauseSettings(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIQuotaAutoPauseSettingsDBTimeout)
	defer cancel()

	settings := QuotaAutoPauseSettings{}
	ttl := openAIQuotaAutoPauseSettingsCacheTTL
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpsAdvancedSettings)
	if err == nil {
		settings = s.decode(raw)
	} else if !errors.Is(err, s.notFound) {
		// Retain cached settings on read failure and retry after a shorter interval.
		if prior, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); prior != nil {
			settings = prior.settings
		}
		ttl = openAIQuotaAutoPauseSettingsErrorTTL
	}

	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

func (s *QuotaSettingsCache) SetOpenAIQuotaAutoPauseSettings(settings QuotaAutoPauseSettings) {
	if s == nil {
		return
	}
	settings.DefaultThreshold5h = quotaClamp(settings.DefaultThreshold5h)
	settings.DefaultThreshold7d = quotaClamp(settings.DefaultThreshold7d)
	s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
		settings:  settings,
		expiresAt: time.Now().Add(openAIQuotaAutoPauseSettingsCacheTTL).UnixNano(),
	})
}

// Apply publishes explicit thresholds; otherwise it expires the cache for a later background refresh.
func (s *QuotaSettingsCache) Apply(value QuotaAutoPauseSettings, explicit bool) {
	s.openAIQuotaAutoPauseSettingsSF.Forget(openAIQuotaAutoPauseSettingsRefreshKey)
	if explicit {
		s.SetOpenAIQuotaAutoPauseSettings(value)
	} else if cached, _ := s.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); cached != nil {
		s.openAIQuotaAutoPauseSettingsCache.Store(&cachedOpenAIQuotaAutoPauseSettings{
			settings:  cached.settings,
			expiresAt: 0,
		})
	}
}
