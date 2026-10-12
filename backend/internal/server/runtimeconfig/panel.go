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

// Panel middleware needs limits on every request, while administrator reads
// need the stored value directly. Cached reads reuse an unexpired snapshot or
// join one singleflight load. A failed load briefly republishes the entry
// observed during recovery, or supplies defaults if no entry exists. Successful
// reads normalize stored numeric rates into the range accepted by writes.
//
// Each cache publication combines settings with one wall-clock expiry in an
// immutable entry. Shared refresh work retains request values while replacing
// cancellation with its own database context. Saves write storage before their
// local publication and bypass the refresh group, so an older in-flight read
// can subsequently replace that cache entry. Other instances observe changes
// according to their own cache lifetimes.

const (
	// SettingKeyPanelRateLimitSettings stores the JSON object read by admin
	// endpoints and the middleware cache when their next storage query runs.
	SettingKeyPanelRateLimitSettings = "panel_rate_limit_settings"

	// panelRateLimitRPMMax caps each per-minute bucket so saved-input validation
	// and direct-read normalization apply the same supported numeric range.
	panelRateLimitRPMMax = 100000

	// These durations govern healthy reuse, error recovery, and the detached
	// database context used by the singleflight owner during a cache refresh.
	panelRateLimitCacheTTL  = 60 * time.Second // successful cache-entry lifetime
	panelRateLimitErrorTTL  = 5 * time.Second  // failed-refresh fallback lifetime
	panelRateLimitDBTimeout = 5 * time.Second  // shared storage-query context budget
)

// PanelSettingsRepository supplies persistent JSON values to panel reads and validated writes.
type PanelSettingsRepository interface {
	// GetValue retrieves raw settings using the supplied operation context.
	// Missing keys return ErrSettingNotFound so direct reads can select defaults;
	// other failures remain available to administrator and cached-read consumers.
	GetValue(context.Context, string) (string, error)
	// Set persists serialized settings using the supplied operation context.
	// A successful return permits the caller's cache publication; an error
	// leaves that caller's prepared snapshot unpublished for subsequent middleware reads.
	Set(context.Context, string, string) error
}

// PanelSettings owns the panel rate limit cache shared by HTTP consumers.
type PanelSettings struct {
	// settingRepo remains caller-owned. Cached reads tolerate nil storage with
	// defaults, while direct reads and writes require an initialized repository.
	settingRepo PanelSettingsRepository
	// panelRateLimitCache and panelRateLimitSF separate publication from shared
	// loading. Cache loads coalesce, while saves publish independently; each
	// pointer holds fully constructed settings and expiry that readers leave immutable.
	panelRateLimitCache atomic.Value       // immutable current entry pointer
	panelRateLimitSF    singleflight.Group // shared cache-miss load coordination
}

// PanelRateLimitSettings controls user and public-IP buckets in PanelRateLimiter.
// Authenticated requests are keyed by user ID. Its public-IP consumer checks
// global-unicast eligibility and excludes private, loopback, link-local, and
// unspecified addresses before applying that separate bucket.
type PanelRateLimitSettings struct {
	// Enabled controls both authenticated and public-IP middleware limits. False
	// lets those consumers continue requests without performing their counter checks.
	Enabled bool `json:"enabled"`
	// UserRPM, HeavyRPM, and PublicIPRPM are requests per minute for their named
	// buckets. Zero disables a bucket; direct reads clamp rates into the supported
	// range and writes reject counts outside that range.
	UserRPM  int `json:"user_rpm"`  // authenticated user's aggregate limit
	HeavyRPM int `json:"heavy_rpm"` // authenticated user's heavy-query limit
	// ExemptAdmin bypasses both user-scoped buckets for authenticated administrator
	// roles. The public-IP consumer applies its separate eligibility and rate settings.
	ExemptAdmin bool `json:"exempt_admin"`
	PublicIPRPM int  `json:"public_ip_rpm"` // unauthenticated public-address limit
}

// cachedPanelRateLimitSettings holds one immutable value and expiry published to cache readers.
type cachedPanelRateLimitSettings struct {
	// settings and expiresAt are published together in one atomic cache pointer.
	// Readers compare wall-clock Unix nanoseconds and copy settings by value;
	// later refreshes replace the pointer instead of changing these fields.
	settings  PanelRateLimitSettings // immutable copied bucket settings
	expiresAt int64                  // expiry in Unix nanoseconds
}

// NewPanelSettings creates an empty on-demand cache for the supplied repository.
// Nil storage is accepted for cached default reads; direct Get and Set require
// initialized storage. The caller owns repository lifetime, and construction
// performs no settings prefetch.
func NewPanelSettings(repo PanelSettingsRepository) *PanelSettings {
	return &PanelSettings{settingRepo: repo}
}

// DefaultPanelRateLimitSettings returns fresh enabled limits with administrator exemptions.
// User, heavy-query, and public-IP counts are 240, 60, and 300 per minute.
// Callers can modify the returned primitive fields independently of later default reads.
func DefaultPanelRateLimitSettings() *PanelRateLimitSettings {
	return &PanelRateLimitSettings{
		Enabled:     true,
		UserRPM:     240,
		HeavyRPM:    60,
		ExemptAdmin: true,
		PublicIPRPM: 300,
	}
}

// normalizePanelRateLimitSettings clamps all three per-minute counts into the supported range.
// Nil input is ignored, while enable and exemption flags retain their supplied
// values. Negative counts become disabled buckets, and oversized counts reach
// the same ceiling validated by writes.
func normalizePanelRateLimitSettings(s *PanelRateLimitSettings) {
	if s == nil {
		return
	}
	// Treat negative persisted counts as disabled middleware buckets.
	if s.UserRPM < 0 {
		s.UserRPM = 0
	}
	if s.HeavyRPM < 0 {
		s.HeavyRPM = 0
	}
	if s.PublicIPRPM < 0 {
		s.PublicIPRPM = 0
	}
	// Keep read-side counts within the ceiling also validated during saves.
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

// GetPanelRateLimitSettings returns a direct storage read normalized for panel consumers.
// Missing keys, blank text, and JSON decoding failures produce fresh defaults;
// other storage errors return to the caller. Valid JSON starts from a zero
// struct, so omitted fields and a null JSON value leave fields at zero. Supply
// an initialized repository and operation context for direct reads.
func (s *PanelSettings) GetPanelRateLimitSettings(ctx context.Context) (*PanelRateLimitSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPanelRateLimitSettings)
	if err != nil {
		// Absence restores the enabled default policy. Other repository failures
		// remain errors so administrator reads can report unavailable settings storage.
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
		// An undecodable stored value supplies no usable policy. Return known
		// defaults while logging its parse failure for the administrator to investigate.
		slog.Warn("failed to unmarshal panel rate limit settings, falling back to defaults",
			"error", err, "key", SettingKeyPanelRateLimitSettings)
		return DefaultPanelRateLimitSettings(), nil
	}
	normalizePanelRateLimitSettings(settings)
	return settings, nil
}

// SetPanelRateLimitSettings persists validated limits and publishes a fresh instance entry.
// Nil input, negative rates, and rates above the supported ceiling return errors
// before storage is written. Keep the supplied settings unchanged until this
// method returns because JSON serialization and the later cache copy read it
// in separate phases.
//
// Storage accepts the JSON before local cache publication. A failed write
// publishes nothing from this call, and success copies primitive fields into
// the cache with the normal lifetime. Independent cached reloads can subsequently
// publish older read results; other instances refresh according to their own
// cache lifetimes.
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

	// The persistent write has succeeded before this publication. Middleware
	// can use the saved value locally while other instances await their own refresh.
	s.storePanelRateLimitCache(*settings, panelRateLimitCacheTTL)
	return nil
}

// GetPanelRateLimitSettingsCached returns an unexpired value or joins one shared storage load.
// Nil receivers or repositories return defaults, and nil ctx is accepted. A
// successful read is cached for sixty seconds; a failed read retains the entry
// visible during fallback selection, or defaults, for five seconds before another
// query is eligible.
//
// The load keeps context values but uses an independent five-second database
// context. Singleflight waiters wait for its return even when their request is
// cancelled. Concurrent saves can be followed by an older in-flight read's
// publication, so this cache provides timed reuse rather than storage-version
// ordering between reads and writes.
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
		// Another reader or save may publish while this caller waits for
		// singleflight ownership. Reuse that fresh entry before starting another query.
		if cached, ok := s.panelRateLimitCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.settings, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		// Carry request values into the shared query while replacing cancellation
		// and deadline; one disconnected requester cannot cancel work shared by waiters.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), panelRateLimitDBTimeout)
		defer cancel()

		settings, err := s.GetPanelRateLimitSettings(dbCtx)
		if err != nil {
			slog.Warn("failed to get panel rate limit settings", "error", err)
			// Use the local entry observed after this failed read, including a value
			// published during the query. The shorter lifetime makes storage recovery
			// eligible sooner; a save racing this selection can still be overwritten.
			fallback := *DefaultPanelRateLimitSettings()
			if prior, ok := s.panelRateLimitCache.Load().(*cachedPanelRateLimitSettings); ok && prior != nil {
				fallback = prior.settings
			}
			s.storePanelRateLimitCache(fallback, panelRateLimitErrorTTL)
			return fallback, nil
		}

		// Singleflight excludes other loads but does not cover saves. Publish this
		// fetched snapshot directly, even if a concurrent save published a newer one.
		s.storePanelRateLimitCache(*settings, panelRateLimitCacheTTL)
		return *settings, nil
	})
	if settings, ok := result.(PanelRateLimitSettings); ok {
		return settings
	}
	// The defensive type check supplies concrete defaults for an unexpected
	// shared value, keeping each bucket available for middleware policy evaluation.
	return *DefaultPanelRateLimitSettings()
}

// storePanelRateLimitCache publishes copied settings and their wall-clock expiration.
// The lifetime starts while the entry is constructed. Supply a positive ttl
// for reuse; readers leave the published fields immutable and later updates
// replace the complete pointer.
func (s *PanelSettings) storePanelRateLimitCache(settings PanelRateLimitSettings, ttl time.Duration) {
	s.panelRateLimitCache.Store(&cachedPanelRateLimitSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
