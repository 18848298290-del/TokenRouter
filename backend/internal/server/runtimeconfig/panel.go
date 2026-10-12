package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

const (
	// SettingKeyPanelRateLimitSettings stores the panel rate limit JSON.
	SettingKeyPanelRateLimitSettings = "panel_rate_limit_settings"

	// panelRateLimitRPMMax bounds accepted requests-per-minute settings.
	panelRateLimitRPMMax = 100000

	panelRateLimitCacheTTL  = 60 * time.Second
	panelRateLimitErrorTTL  = 5 * time.Second
	panelRateLimitDBTimeout = 5 * time.Second
)

// PanelSettingsRepository reads and writes panel rate limit settings.
type PanelSettingsRepository interface {
	GetValue(context.Context, string) (string, error)
	Set(context.Context, string, string) error
}

// PanelSettings owns the panel rate limit cache shared by HTTP consumers.
type PanelSettings struct {
	settingRepo         PanelSettingsRepository
	panelRateLimitCache atomic.Value
	panelRateLimitSF    singleflight.Group
}

// PanelRateLimitSettings configures panel API rate limits.
// Authenticated endpoints use user ID buckets so users behind a proxy remain independent.
// Public endpoints use security client IP buckets, skipping private and loopback addresses.
type PanelRateLimitSettings struct {
	// Enabled switches all panel rate limits on or off.
	Enabled bool `json:"enabled"`
	// UserRPM limits authenticated requests per user per minute; zero disables this limit.
	UserRPM int `json:"user_rpm"`
	// HeavyRPM limits aggregate queries per user per minute; zero disables this limit.
	HeavyRPM int `json:"heavy_rpm"`
	// ExemptAdmin exempts administrators from user-scoped limits.
	ExemptAdmin bool `json:"exempt_admin"`
	// PublicIPRPM limits unauthenticated requests per IP per minute; zero disables this limit.
	PublicIPRPM int `json:"public_ip_rpm"`
}

// cachedPanelRateLimitSettings stores settings and their expiration time.
type cachedPanelRateLimitSettings struct {
	settings  PanelRateLimitSettings
	expiresAt int64 // Unix nanoseconds
}

// NewPanelSettings creates an empty cache; settings are fetched on demand.
func NewPanelSettings(repo PanelSettingsRepository) *PanelSettings {
	return &PanelSettings{settingRepo: repo}
}

// DefaultPanelRateLimitSettings returns enabled panel limits with administrator exemptions.
func DefaultPanelRateLimitSettings() *PanelRateLimitSettings {
	return &PanelRateLimitSettings{
		Enabled:     true,
		UserRPM:     240,
		HeavyRPM:    60,
		ExemptAdmin: true,
		PublicIPRPM: 300,
	}
}

// normalizePanelRateLimitSettings clamps RPM values to the supported range.
func normalizePanelRateLimitSettings(s *PanelRateLimitSettings) {
	if s == nil {
		return
	}
	if s.UserRPM < 0 {
		s.UserRPM = 0
	}
	if s.HeavyRPM < 0 {
		s.HeavyRPM = 0
	}
	if s.PublicIPRPM < 0 {
		s.PublicIPRPM = 0
	}
	if s.UserRPM > panelRateLimitRPMMax {
		s.UserRPM = panelRateLimitRPMMax
	}
	if s.HeavyRPM > panelRateLimitRPMMax {
		s.HeavyRPM = panelRateLimitRPMMax
	}
	if s.PublicIPRPM > panelRateLimitRPMMax {
		s.PublicIPRPM = panelRateLimitRPMMax
	}
}

// GetPanelRateLimitSettings reads panel limits directly from storage.
// Missing, blank, or malformed values use defaults; other storage errors are returned.
func (s *PanelSettings) GetPanelRateLimitSettings(ctx context.Context) (*PanelRateLimitSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPanelRateLimitSettings)
	if err != nil {
		if errors.Is(err, settings.ErrSettingNotFound) {
			return DefaultPanelRateLimitSettings(), nil
		}
		return nil, fmt.Errorf("get panel rate limit settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return DefaultPanelRateLimitSettings(), nil
	}

	settings := &PanelRateLimitSettings{}
	if err := json.Unmarshal([]byte(value), settings); err != nil {
		slog.Warn("failed to unmarshal panel rate limit settings, falling back to defaults",
			"error", err, "key", SettingKeyPanelRateLimitSettings)
		return DefaultPanelRateLimitSettings(), nil
	}
	normalizePanelRateLimitSettings(settings)
	return settings, nil
}

// SetPanelRateLimitSettings saves limits and refreshes this process's cache.
// Other instances read the change after their cached entry expires.
func (s *PanelSettings) SetPanelRateLimitSettings(ctx context.Context, settings *PanelRateLimitSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	if settings.UserRPM < 0 || settings.HeavyRPM < 0 || settings.PublicIPRPM < 0 {
		return fmt.Errorf("rate limit values cannot be negative")
	}
	if settings.UserRPM > panelRateLimitRPMMax || settings.HeavyRPM > panelRateLimitRPMMax || settings.PublicIPRPM > panelRateLimitRPMMax {
		return fmt.Errorf("rate limit values must be at most %d", panelRateLimitRPMMax)
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal panel rate limit settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyPanelRateLimitSettings, string(data)); err != nil {
		return err
	}

	s.storePanelRateLimitCache(*settings, panelRateLimitCacheTTL)
	return nil
}

// GetPanelRateLimitSettingsCached returns limits cached for 60 seconds.
// On storage errors it returns the last cached value, or defaults if none exists,
// and retries after a five-second cache lifetime.
func (s *PanelSettings) GetPanelRateLimitSettingsCached(ctx context.Context) PanelRateLimitSettings {
	if s == nil || s.settingRepo == nil {
		return *DefaultPanelRateLimitSettings()
	}
	if cached, ok := s.panelRateLimitCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.settings
		}
	}

	result, _, _ := s.panelRateLimitSF.Do("panel_rate_limit_settings", func() (any, error) {
		// A preceding refresh may have populated the cache before this call entered singleflight.
		if cached, ok := s.panelRateLimitCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.settings, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		// Detach cancellation so a disconnected request cannot cancel the shared cache refresh.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), panelRateLimitDBTimeout)
		defer cancel()

		settings, err := s.GetPanelRateLimitSettings(dbCtx)
		if err != nil {
			slog.Warn("failed to get panel rate limit settings", "error", err)
			// Keep the last known settings and retry after the shorter error TTL.
			fallback := *DefaultPanelRateLimitSettings()
			if prior, ok := s.panelRateLimitCache.Load().(*cachedPanelRateLimitSettings); ok && prior != nil {
				fallback = prior.settings
			}
			s.storePanelRateLimitCache(fallback, panelRateLimitErrorTTL)
			return fallback, nil
		}

		s.storePanelRateLimitCache(*settings, panelRateLimitCacheTTL)
		return *settings, nil
	})
	if settings, ok := result.(PanelRateLimitSettings); ok {
		return settings
	}
	return *DefaultPanelRateLimitSettings()
}

func (s *PanelSettings) storePanelRateLimitCache(settings PanelRateLimitSettings, ttl time.Duration) {
	s.panelRateLimitCache.Store(&cachedPanelRateLimitSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
