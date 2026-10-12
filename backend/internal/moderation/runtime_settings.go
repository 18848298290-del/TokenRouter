package moderation

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	cyberSessionBlockRuntimeCacheTTL  = 60 * time.Second
	cyberSessionBlockRuntimeErrorTTL  = 5 * time.Second
	cyberSessionBlockRuntimeDBTimeout = 5 * time.Second
)

// RuntimeSettings caches Cyber session-block enablement and TTL.
type RuntimeSettings struct {
	settingRepo            RuntimeSettingsStore
	notFound               error
	cyberSessionBlockCache atomic.Value
	cyberSessionBlockSF    singleflight.Group
}
type RuntimeSettingsStore interface {
	GetValue(context.Context, string) (string, error)
}

type cachedCyberSessionBlockRuntime struct {
	enabled   bool
	ttl       time.Duration
	expiresAt int64 // Unix nanoseconds.
}

// NewRuntimeSettings defers storage reads until GetCyberSessionBlockRuntime is called.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

func (s *RuntimeSettings) GetCyberSessionBlockRuntime(ctx context.Context) (bool, time.Duration) {
	if s == nil || s.settingRepo == nil {
		return false, time.Hour
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, ok := s.cyberSessionBlockCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled, cached.ttl
		}
	}
	result, _, _ := s.cyberSessionBlockSF.Do("cyber_session_block_runtime", func() (any, error) {
		if cached, ok := s.cyberSessionBlockCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cyberSessionBlockRuntimeDBTimeout)
		defer cancel()

		enabledVal, enabledErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockEnabled)
		ttlVal, ttlErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockTTLSeconds)
		if enabledErr != nil && !errors.Is(enabledErr, s.notFound) {
			slog.Warn("failed to get cyber session block setting", "error", enabledErr)
			entry := &cachedCyberSessionBlockRuntime{
				enabled:   false,
				ttl:       time.Hour,
				expiresAt: time.Now().Add(cyberSessionBlockRuntimeErrorTTL).UnixNano(),
			}
			s.cyberSessionBlockCache.Store(entry)
			return entry, nil
		}

		ttl := time.Hour
		if ttlErr == nil {
			if seconds, err := strconv.Atoi(strings.TrimSpace(ttlVal)); err == nil && seconds > 0 {
				ttl = time.Duration(seconds) * time.Second
			}
		}
		entry := &cachedCyberSessionBlockRuntime{
			enabled:   enabledErr == nil && strings.TrimSpace(enabledVal) == "true",
			ttl:       ttl,
			expiresAt: time.Now().Add(cyberSessionBlockRuntimeCacheTTL).UnixNano(),
		}
		s.cyberSessionBlockCache.Store(entry)
		return entry, nil
	})
	if entry, ok := result.(*cachedCyberSessionBlockRuntime); ok && entry != nil {
		return entry.enabled, entry.ttl
	}
	return false, time.Hour
}
