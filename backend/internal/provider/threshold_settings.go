package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	AnthropicFableRateLimitKey = "claude-fable-5"

	// SettingKeyProviderSchedulingThresholds stores scheduling limits separately from accumulated spending.
	SettingKeyProviderSchedulingThresholds = "provider_scheduling_thresholds"

	providerSchedulingThresholdsCacheTTL  = 60 * time.Second
	providerSchedulingThresholdsErrorTTL  = 5 * time.Second
	providerSchedulingThresholdsDBTimeout = 5 * time.Second
)

// AllowedSchedulingThresholdPlatforms lists platforms with configurable usage-window thresholds.
// OpenAI, Anthropic, and Grok expose native windows; Kimi and Zhipu expose Coding Plan windows.
// DeepSeek uses balance checks.
var AllowedSchedulingThresholdPlatforms = []string{
	PlatformOpenAI,
	PlatformAnthropic,
	PlatformGrok,
	PlatformKimi,
	PlatformZhipu,
}

// cachedProviderSchedulingThresholds holds platform thresholds and their expiration time.
type cachedProviderSchedulingThresholds struct {
	thresholds map[string]int
	expiresAt  int64 // Unix nanoseconds
}

func DefaultProviderSchedulingThresholds() map[string]int {
	return map[string]int{
		PlatformOpenAI:    100,
		PlatformAnthropic: 100,
		PlatformGrok:      100,
	}
}

func ValidateAndNormalizeProviderSchedulingThresholds(input map[string]int) (map[string]int, error) {
	normalized := DefaultProviderSchedulingThresholds()
	for platform, value := range input {
		allowed := slices.Contains(AllowedSchedulingThresholdPlatforms, platform)
		if !allowed {
			return nil, apperror.BadRequest("INVALID_PROVIDER_SCHEDULING_THRESHOLDS", fmt.Sprintf("unknown platform %q", platform))
		}
		if value < 1 || value > 100 {
			return nil, apperror.BadRequest("INVALID_PROVIDER_SCHEDULING_THRESHOLDS", "platform scheduling threshold must be between 1 and 100")
		}
		normalized[platform] = value
	}
	return normalized, nil
}

func ParseProviderSchedulingThresholdsSetting(raw string) (map[string]int, error) {
	thresholds := DefaultProviderSchedulingThresholds()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return thresholds, nil
	}
	parsed := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return thresholds, err
	}
	for _, platform := range AllowedSchedulingThresholdPlatforms {
		if value, ok := parsed[platform]; ok {
			thresholds[platform] = BoundedIntOrDefault(value, 1, 100, 100)
		}
	}
	return thresholds, nil
}

func BoundedIntOrDefault(value, minValue, maxValue, defaultValue int) int {
	if value < minValue || value > maxValue {
		return defaultValue
	}
	return value
}

func CloneProviderSchedulingThresholds(input map[string]int) map[string]int {
	if len(input) == 0 {
		return DefaultProviderSchedulingThresholds()
	}
	cloned := make(map[string]int, len(input))
	maps.Copy(cloned, input)
	return cloned
}

// GetProviderSchedulingThresholds coalesces reads, caches defaults briefly on read errors, and returns an
// independent map.
func (s *RuntimeSettings) GetProviderSchedulingThresholds(ctx context.Context) map[string]int {
	if s == nil || s.settingRepo == nil {
		return DefaultProviderSchedulingThresholds()
	}
	if cached, ok := s.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds); ok {
		if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
			return CloneProviderSchedulingThresholds(cached.thresholds)
		}
	}

	result, err, _ := s.providerSchedulingThresholdsSF.Do(SettingKeyProviderSchedulingThresholds, func() (any, error) {
		if cached, ok := s.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds); ok {
			if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
				return CloneProviderSchedulingThresholds(cached.thresholds), nil
			}
		}

		thresholds := DefaultProviderSchedulingThresholds()
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerSchedulingThresholdsDBTimeout)
		defer cancel()

		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyProviderSchedulingThresholds)
		if err != nil {
			if errors.Is(err, s.notFound) {
				// A missing setting caches defaults for the normal TTL.
				s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
					thresholds: CloneProviderSchedulingThresholds(thresholds),
					expiresAt:  time.Now().Add(providerSchedulingThresholdsCacheTTL).UnixNano(),
				})
				return CloneProviderSchedulingThresholds(thresholds), nil
			}
			slog.Warn("failed to get provider scheduling thresholds, falling back to defaults", "error", err)
			s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
				thresholds: CloneProviderSchedulingThresholds(thresholds),
				expiresAt:  time.Now().Add(providerSchedulingThresholdsErrorTTL).UnixNano(),
			})
			return CloneProviderSchedulingThresholds(thresholds), nil
		}

		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			if parsed, err := ParseProviderSchedulingThresholdsSetting(trimmed); err != nil {
				slog.Warn("failed to parse provider scheduling thresholds, falling back to defaults", "error", err)
			} else {
				thresholds = parsed
			}
		}

		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
			thresholds: CloneProviderSchedulingThresholds(thresholds),
			expiresAt:  time.Now().Add(providerSchedulingThresholdsCacheTTL).UnixNano(),
		})
		return CloneProviderSchedulingThresholds(thresholds), nil
	})
	if err != nil {
		return DefaultProviderSchedulingThresholds()
	}
	if thresholds, ok := result.(map[string]int); ok {
		return CloneProviderSchedulingThresholds(thresholds)
	}
	return DefaultProviderSchedulingThresholds()
}

// ApplySchedulingThresholds publishes committed settings. Call it after saving; nil invalidates the cache.
func (s *RuntimeSettings) ApplySchedulingThresholds(value map[string]int) {
	s.providerSchedulingThresholdsSF.Forget(SettingKeyProviderSchedulingThresholds)
	if value != nil {
		normalizedThresholds, err := ValidateAndNormalizeProviderSchedulingThresholds(value)
		if err != nil {
			normalizedThresholds = DefaultProviderSchedulingThresholds()
		}
		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
			thresholds: CloneProviderSchedulingThresholds(normalizedThresholds),
			expiresAt:  time.Now().Add(providerSchedulingThresholdsCacheTTL).UnixNano(),
		})
	} else {
		// Reload from storage on the next read when a partial update omits thresholds.
		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{})
	}
}
