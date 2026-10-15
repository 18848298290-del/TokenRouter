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

// threshold_settings.go validates platform scheduling thresholds and caches
// their persisted runtime settings.
// Platform pause thresholds are stored as a JSON map, while provider
// credentials can supply a separate override. The default map disables
// OpenAI, Anthropic, and Grok with one hundred percent and leaves Coding Plan
// platforms absent. Administrator validation requires exact supported
// platform keys, whereas persisted parsing ignores unknown keys and replaces
// out-of-range recognized values with the disabled threshold.
//
// Runtime reads reuse immutable process observations and coalesce expired
// lookups. A missing key caches defaults for the normal lifetime; a storage
// failure also selects defaults but shortens retry timing, and malformed JSON
// uses the normal lifetime. Returned maps are copied so callers cannot mutate
// a cached observation. Applying committed settings publishes another
// observation or invalidates it for reload; a forgotten earlier flight can
// still finish and publish afterward.

// The Fable scope key, settings key, cache lifetimes, and read budget connect
// platform threshold decisions with model blocking and persisted runtime
// configuration.
const (
	AnthropicFableRateLimitKey = "claude-fable-5" // Fable model block scope

	// SettingKeyProviderSchedulingThresholds stores scheduling limits separately
	// from accumulated spending.
	SettingKeyProviderSchedulingThresholds = "provider_scheduling_thresholds"

	providerSchedulingThresholdsCacheTTL  = 60 * time.Second // ordinary observation lifetime
	providerSchedulingThresholdsErrorTTL  = 5 * time.Second  // storage failure retry delay
	providerSchedulingThresholdsDBTimeout = 5 * time.Second  // detached storage read budget
)

// AllowedSchedulingThresholdPlatforms lists the platform keys accepted for
// usage-window settings and evaluation. Keep its shared slice stable while
// validators, parsers, and evaluators read it.
var AllowedSchedulingThresholdPlatforms = []string{
	PlatformOpenAI,
	PlatformAnthropic,
	PlatformGrok,
	PlatformKimi,
	PlatformZhipu,
}

// cachedProviderSchedulingThresholds holds an immutable platform map and its
// wall-clock expiration for runtime readers.
type cachedProviderSchedulingThresholds struct {
	// thresholds is copied when published and returned through copying helpers.
	// Callers must leave the stored map unchanged after its entry becomes
	// visible.
	thresholds map[string]int // immutable platform percentage map
	expiresAt  int64          // Unix nanoseconds
}

// DefaultProviderSchedulingThresholds returns a new map disabling OpenAI,
// Anthropic, and Grok with one hundred percent. Kimi and Zhipu remain absent
// so their evaluators skip the platform threshold unless another setting or
// credential override supplies it.
func DefaultProviderSchedulingThresholds() map[string]int {
	return map[string]int{
		PlatformOpenAI:    100,
		PlatformAnthropic: 100,
		PlatformGrok:      100,
	}
}

// ValidateAndNormalizeProviderSchedulingThresholds returns a new
// default-based map with supported input values applied. Platform keys match
// exactly and values require one through one hundred; unknown keys or invalid
// percentages return a bad-request error.
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
		// One hundred keeps a supported platform configured while disabling its
		// evaluator, so preserve it alongside active percentage thresholds.
		normalized[platform] = value
	}
	return normalized, nil
}

// ParseProviderSchedulingThresholdsSetting decodes persisted JSON over a new
// default map. Blank input uses defaults; malformed JSON returns defaults
// with its error, unknown keys are ignored, and recognized out-of-range
// values become one hundred.
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
			// Persisted values may bypass administrator validation. Invalid recognized
			// percentages select the disabled gate instead of an unvalidated active
			// threshold.
			thresholds[platform] = BoundedIntOrDefault(value, 1, 100, 100)
		}
	}
	return thresholds, nil
}

// BoundedIntOrDefault returns value inside inclusive bounds, otherwise
// returning defaultValue unchanged. Callers select a valid default because
// the fallback itself is not checked against the supplied bounds.
func BoundedIntOrDefault(value, minValue, maxValue, defaultValue int) int {
	if value < minValue || value > maxValue {
		return defaultValue
	}
	return value
}

// CloneProviderSchedulingThresholds copies a nonempty scalar map so callers
// can change their result independently. Nil or empty input selects a new
// default map, including the disabled native-platform thresholds.
func CloneProviderSchedulingThresholds(input map[string]int) map[string]int {
	if len(input) == 0 {
		return DefaultProviderSchedulingThresholds()
	}
	cloned := make(map[string]int, len(input))
	maps.Copy(cloned, input)
	return cloned
}

// GetProviderSchedulingThresholds returns a separate platform threshold map,
// synchronously loading an expired or absent observation. Nil receivers or
// repositories select defaults; a configured read retains context values
// while detaching cancellation and using a five-second budget.
//
// Singleflight joins concurrent misses, and the executing closure rechecks
// the cache before storage. Missing keys use normal-lived defaults; actual
// storage failures warn and shorten their lifetime, while malformed JSON
// warns and caches defaults normally. Every path copies maps before handing
// them to readers. Waiting callers remain joined until the shared closure
// finishes, even when their contexts are cancelled.
func (s *RuntimeSettings) GetProviderSchedulingThresholds(ctx context.Context) map[string]int {
	if s == nil || s.settingRepo == nil {
		return DefaultProviderSchedulingThresholds()
	}
	if cached, ok := s.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds); ok {
		if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
			return CloneProviderSchedulingThresholds(cached.thresholds)
		}
	}

	// Another caller can publish after the initial cache check. Recheck inside
	// the selected flight so a now-fresh observation avoids another storage
	// read.
	result, err, _ := s.providerSchedulingThresholdsSF.Do(SettingKeyProviderSchedulingThresholds, func() (any, error) {
		if cached, ok := s.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds); ok {
			if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
				return CloneProviderSchedulingThresholds(cached.thresholds), nil
			}
		}

		thresholds := DefaultProviderSchedulingThresholds()
		ttl := providerSchedulingThresholdsCacheTTL
		// Give shared storage work its own budget so cancelling the initiating
		// request does not terminate the observation awaited by other callers.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerSchedulingThresholdsDBTimeout)
		defer cancel()

		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyProviderSchedulingThresholds)
		if err != nil {
			// An absent key is a stable unconfigured state and retains defaults for
			// the normal lifetime. Unavailable storage retries sooner so a recovered
			// store can supply configured percentages on a later selection read.
			if !errors.Is(err, s.notFound) {
				slog.Warn("failed to get provider scheduling thresholds, falling back to defaults", "error", err)
				ttl = providerSchedulingThresholdsErrorTTL
			}
		} else if trimmed := strings.TrimSpace(raw); trimmed != "" {
			if parsed, err := ParseProviderSchedulingThresholdsSetting(trimmed); err != nil {
				// Discard malformed stored JSON as a complete setting; initialized
				// defaults remain available and use the ordinary cache lifetime.
				slog.Warn("failed to parse provider scheduling thresholds, falling back to defaults", "error", err)
			} else {
				thresholds = parsed
			}
		}

		// Sample expiration after storage and parsing finish, then publish a
		// private map. Returned copies let readers adjust their own settings.
		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
			thresholds: CloneProviderSchedulingThresholds(thresholds),
			expiresAt:  time.Now().Add(ttl).UnixNano(),
		})
		return CloneProviderSchedulingThresholds(thresholds), nil
	})
	// An unexpected flight error or result shape still supplies default
	// percentages, keeping native-platform gates disabled and Coding Plan
	// platform settings absent until another usable observation is read.
	if err != nil {
		return DefaultProviderSchedulingThresholds()
	}
	if thresholds, ok := result.(map[string]int); ok {
		return CloneProviderSchedulingThresholds(thresholds)
	}
	return DefaultProviderSchedulingThresholds()
}

// ApplySchedulingThresholds publishes committed platform settings or
// invalidates this runtime cache when value is nil. Call it on a nonnil
// instance after saving; invalid nonnil input selects defaults, and
// successful input is normalized and copied.
//
// Forget permits later readers to start a new flight while existing reads
// continue. Those closures can still Store their own results after
// application, so publication is not ordered against earlier refresh work.
// Nil input installs an empty observation, making later Get calls enter the
// synchronous refresh path until a fresh entry is published.
//
// Composite runtime application supplies a post-commit readback map. That
// reader supplies nonnil thresholds even when the update omitted the field,
// so this consumer republishes the persisted percentages through the nonnil
// branch.
func (s *RuntimeSettings) ApplySchedulingThresholds(value map[string]int) {
	// Remove the key from the flight lookup before installing the application
	// entry. An existing closure still holds its runtime pointer and stores
	// without a publication-generation check.
	s.providerSchedulingThresholdsSF.Forget(SettingKeyProviderSchedulingThresholds)
	if value != nil {
		normalizedThresholds, err := ValidateAndNormalizeProviderSchedulingThresholds(value)
		if err != nil {
			// Invalid direct application chooses the complete disabled defaults
			// instead of publishing a partially normalized map to scheduling readers.
			normalizedThresholds = DefaultProviderSchedulingThresholds()
		}
		// Copy normalized percentages before publication so the cached entry
		// remains stable while later callers modify their separately returned maps.
		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{
			thresholds: CloneProviderSchedulingThresholds(normalizedThresholds),
			expiresAt:  time.Now().Add(providerSchedulingThresholdsCacheTTL).UnixNano(),
		})
	} else {
		// Nil application installs an invalid observation for shared refresh.
		// Readers that find the empty entry enter that path, whose cache recheck
		// can reuse another publisher's fresh entry.
		s.providerSchedulingThresholdsCache.Store(&cachedProviderSchedulingThresholds{})
	}
}
