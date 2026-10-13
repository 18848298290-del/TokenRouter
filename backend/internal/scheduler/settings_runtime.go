package scheduler

import (
	"context"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Advanced scheduler settings are shared by production selection and
// administrative diagnostics through one short-lived cache. A load reads the
// complete runtime key set, parses valid overrides, and publishes scalar
// values with an isolated weight map. Feedback and escape parse failures keep
// the supplied process values. Invalid sample-size and weight text leaves
// those overrides absent for later effective-policy resolution, and the two
// ordinary strategy switches default to false.
//
// Expired readers coalesce on one singleflight key and wait for the same
// bounded database operation. That operation survives cancellation of its
// initiating request. Store publishes a prepared settings value and forgets
// the coalescing key, but a load already running can still publish afterward.
// The cache belongs to the instance, so callers supply consistent process
// defaults and use the published values until their five-second lifetime
// expires.

// The setting keys select persisted scheduler overrides, while the durations
// bound cache freshness and shared database read work.
const (
	// weighted affinity setting key.
	SettingKeyAdvancedSchedulerStickyWeightedEnabled = "advanced_scheduler_sticky_weighted_enabled"
	// subscription preference setting key.
	SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled = "advanced_scheduler_subscription_priority_enabled"
	// error blending setting key.
	SettingKeyAdvancedSchedulerEWMAErrorRateAlpha = "advanced_scheduler_ewma_error_rate_alpha"
	// TTFT blending setting key.
	SettingKeyAdvancedSchedulerEWMATTFTAlpha = "advanced_scheduler_ewma_ttft_alpha"
	// escape switch setting key.
	SettingKeyAdvancedSchedulerStickyEscapeEnabled = "advanced_scheduler_sticky_escape_enabled"
	// escape latency setting key.
	SettingKeyAdvancedSchedulerStickyEscapeTTFTMs = "advanced_scheduler_sticky_escape_ttft_ms"
	// escape error fraction key.
	SettingKeyAdvancedSchedulerStickyEscapeErrorRate = "advanced_scheduler_sticky_escape_error_rate"
	// sample size setting key.
	SettingKeyAdvancedSchedulerLBTopK = "advanced_scheduler_lb_top_k"
	// priority weight setting key.
	SettingKeyAdvancedSchedulerWeightPriority = "advanced_scheduler_weight_priority"
	// load weight setting key.
	SettingKeyAdvancedSchedulerWeightLoad = "advanced_scheduler_weight_load"
	// queue weight setting key.
	SettingKeyAdvancedSchedulerWeightQueue = "advanced_scheduler_weight_queue"
	// error health weight key.
	SettingKeyAdvancedSchedulerWeightErrorRate = "advanced_scheduler_weight_error_rate"
	// TTFT weight setting key.
	SettingKeyAdvancedSchedulerWeightTTFT = "advanced_scheduler_weight_ttft"
	// reset weight setting key.
	SettingKeyAdvancedSchedulerWeightReset = "advanced_scheduler_weight_reset"
	// quota headroom weight key.
	SettingKeyAdvancedSchedulerWeightQuotaHeadroom = "advanced_scheduler_weight_quota_headroom"
	// previous affinity weight key.
	SettingKeyAdvancedSchedulerWeightPreviousResponse = "advanced_scheduler_weight_previous_response"
	// session affinity weight key.
	SettingKeyAdvancedSchedulerWeightSessionSticky = "advanced_scheduler_weight_session_sticky"
	// published settings reuse duration.
	advancedSchedulerSettingCacheTTL = 5 * time.Second
	// shared source read budget.
	advancedSchedulerSettingDBTimeout = 2 * time.Second
)

// RuntimeSettingSource supplies runtime scheduler text through batch and
// individual key reads.
type RuntimeSettingSource interface {
	// GetMultiple returns values for the requested scheduler keys under the
	// supplied context. SettingsRuntime retries each key when this batch reports
	// an error, using the same operation budget.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// GetValue reads one runtime setting during batch-error recovery under the
	// supplied context. SettingsRuntime omits failed keys and uses the applicable
	// process default when their values are unavailable.
	GetValue(context.Context, string) (string, error)
}

// SettingsRuntime publishes scheduler settings for a shared process instance.
// Published values are immutable within the cache, and returned weight maps
// are copied so per-request overrides can mutate their own map.
type SettingsRuntime struct {
	// cache holds an immutable cachedAdvancedSchedulerSetting pointer.
	// Publication clones weight maps, and Load clones them again before returning
	// values to a caller.
	cache atomic.Value // published parsed settings pointer
	// sf coordinates readers under one settings key. Store forgets that key,
	// allowing later loads to start while an earlier operation can still
	// complete.
	sf          singleflight.Group // settings read coalescing group
	diagnostics Diagnostics        // settings read failure callbacks
}

// cachedAdvancedSchedulerSetting holds one published set of parsed overrides
// and its expiration instant.
type cachedAdvancedSchedulerSetting struct {
	StickyWeightedEnabled       bool // weighted affinity switch value
	SubscriptionPriorityEnabled bool // subscription preference switch value
	LbTopKOverride              int  // positive sample size override
	// WeightOverrides is copied before publication and when returned by Load.
	// Cached readers leave this map unchanged while each caller receives separate
	// mutable storage.
	WeightOverrides map[string]float64 // parsed scoring weight overrides
	// EwmaErrorRateAlpha and EwmaErrorRateAlphaSet carry the parsed error
	// coefficient and successful-override presence together. A false presence
	// flag lets effective settings inherit the process policy.
	EwmaErrorRateAlpha    float64 // error sample blending coefficient
	EwmaErrorRateAlphaSet bool    // valid error coefficient presence
	// EwmaTTFTAlpha and EwmaTTFTAlphaSet carry the parsed TTFT coefficient and
	// successful-override presence together. A false presence flag lets effective
	// settings inherit the process policy.
	EwmaTTFTAlpha    float64 // TTFT sample blending coefficient
	EwmaTTFTAlphaSet bool    // valid TTFT coefficient presence
	// StickyEscapeEnabled and StickyEscapeEnabledSet carry the parsed escape
	// switch and successful-override presence together. A false presence flag
	// lets effective settings inherit the process policy.
	StickyEscapeEnabled    bool // parsed escape switch value
	StickyEscapeEnabledSet bool // valid escape switch presence
	// StickyEscapeTTFTMs and StickyEscapeTTFTMsSet carry the parsed latency
	// threshold and successful-override presence together. A false presence flag
	// lets effective settings inherit the process policy.
	StickyEscapeTTFTMs    float64 // escape TTFT threshold milliseconds
	StickyEscapeTTFTMsSet bool    // valid latency threshold presence
	// StickyEscapeErrorRate and StickyEscapeErrorRateSet carry the parsed error
	// fraction and successful-override presence together. A false presence flag
	// lets effective settings inherit the process policy.
	StickyEscapeErrorRate    float64 // escape error fraction threshold
	StickyEscapeErrorRateSet bool    // valid error threshold presence
	// StickyEscape records the published composite escape value. Load
	// reconstructs its returned policy from the adjacent scalar escape fields
	// rather than reading this cached field.
	StickyEscape policy.StickyEscapeConfig // published composite escape configuration
	// expiresAt is the publication instant plus the cache lifetime. Load compares
	// it with the current clock before reusing any cached setting.
	expiresAt int64 // Unix nanoseconds
}

// WeightOverrideSpec associates a persistent settings key with its shared
// scoring-factor name.
type WeightOverrideSpec struct {
	Key  string // persistent scheduler setting key
	Name string // runtime scoring factor name
}

// NewSettingsRuntime constructs an empty scheduler settings cache with
// supplied diagnostic callbacks.
func NewSettingsRuntime(diagnostics Diagnostics) *SettingsRuntime {
	return &SettingsRuntime{diagnostics: diagnostics}
}

// Load returns parsed scheduler overrides and a caller-owned weight map. Supply
// a nonnil context when a source read may be needed. Fresh
// cache entries are reused regardless of the supplied source or defaults, so
// callers share consistent process defaults for one runtime instance. An
// expired or absent entry invokes one coalesced source read and caches its
// recovered or default values.
//
// The shared read uses a two-second context detached from request
// cancellation, and duplicate callers wait synchronously for it. A failed
// batch retries every known key within that same budget; individual failures
// leave values absent for parsing. Publication clones the weight map, then
// each caller receives another copy. Store can forget a running read's key,
// but that earlier read remains able to publish its result after Store.
func (s *SettingsRuntime) Load(ctx context.Context, repo RuntimeSettingSource, defaults policy.RuntimeSettings) policy.RuntimeSettings {
	if cached, ok := s.cache.Load().(*cachedAdvancedSchedulerSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return policy.RuntimeSettings{
				StickyWeightedEnabled:       cached.StickyWeightedEnabled,
				SubscriptionPriorityEnabled: cached.SubscriptionPriorityEnabled,
				LbTopKOverride:              cached.LbTopKOverride,
				WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(cached.WeightOverrides),
				EwmaErrorRateAlpha:          cached.EwmaErrorRateAlpha,
				EwmaErrorRateAlphaSet:       cached.EwmaErrorRateAlphaSet,
				EwmaTTFTAlpha:               cached.EwmaTTFTAlpha,
				EwmaTTFTAlphaSet:            cached.EwmaTTFTAlphaSet,
				StickyEscapeEnabled:         cached.StickyEscapeEnabled,
				StickyEscapeEnabledSet:      cached.StickyEscapeEnabledSet,
				StickyEscapeTTFTMs:          cached.StickyEscapeTTFTMs,
				StickyEscapeTTFTMsSet:       cached.StickyEscapeTTFTMsSet,
				StickyEscapeErrorRate:       cached.StickyEscapeErrorRate,
				StickyEscapeErrorRateSet:    cached.StickyEscapeErrorRateSet,
				StickyEscape:                policy.StickyEscapeConfig{Enabled: cached.StickyEscapeEnabled, TtftMs: cached.StickyEscapeTTFTMs, ErrorRate: cached.StickyEscapeErrorRate},
			}
		}
	}

	result, _, _ := s.sf.Do("advanced_scheduler_settings", func() (any, error) {
		if cached, ok := s.cache.Load().(*cachedAdvancedSchedulerSetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return policy.RuntimeSettings{
					StickyWeightedEnabled:       cached.StickyWeightedEnabled,
					SubscriptionPriorityEnabled: cached.SubscriptionPriorityEnabled,
					LbTopKOverride:              cached.LbTopKOverride,
					WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(cached.WeightOverrides),
					EwmaErrorRateAlpha:          cached.EwmaErrorRateAlpha,
					EwmaErrorRateAlphaSet:       cached.EwmaErrorRateAlphaSet,
					EwmaTTFTAlpha:               cached.EwmaTTFTAlpha,
					EwmaTTFTAlphaSet:            cached.EwmaTTFTAlphaSet,
					StickyEscapeEnabled:         cached.StickyEscapeEnabled,
					StickyEscapeEnabledSet:      cached.StickyEscapeEnabledSet,
					StickyEscapeTTFTMs:          cached.StickyEscapeTTFTMs,
					StickyEscapeTTFTMsSet:       cached.StickyEscapeTTFTMsSet,
					StickyEscapeErrorRate:       cached.StickyEscapeErrorRate,
					StickyEscapeErrorRateSet:    cached.StickyEscapeErrorRateSet,
					StickyEscape:                policy.StickyEscapeConfig{Enabled: cached.StickyEscapeEnabled, TtftMs: cached.StickyEscapeTTFTMs, ErrorRate: cached.StickyEscapeErrorRate},
				}, nil
			}
		}

		stickyWeightedEnabled := false
		subscriptionPriorityEnabled := false
		lbTopKOverride := 0
		weightOverrides := map[string]float64{}
		ewmaErrorRateAlpha := defaults.EwmaErrorRateAlpha
		ewmaErrorRateAlphaSet := false
		ewmaTTFTAlpha := defaults.EwmaTTFTAlpha
		ewmaTTFTAlphaSet := false
		stickyEscapeEnabled := defaults.StickyEscape.Enabled
		stickyEscapeEnabledSet := false
		stickyEscapeTTFTMs := defaults.StickyEscape.TtftMs
		stickyEscapeTTFTMsSet := false
		stickyEscapeErrorRate := defaults.StickyEscape.ErrorRate
		stickyEscapeErrorRateSet := false
		if repo != nil {
			// Shared readers need the same source result even when the initiating request
			// ends. Detach cancellation while retaining a bounded budget for batch and per-key reads.
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), advancedSchedulerSettingDBTimeout)
			defer cancel()

			values, err := repo.GetMultiple(dbCtx, AdvancedSchedulerRuntimeSettingKeys())
			if err != nil {
				// Recover independently readable settings after a failed batch. Failed individual
				// keys remain absent, so their parsing uses defaults while recovered fields share one TTL.
				s.diagnostics.event("warn", "advanced_scheduler_settings_batch_load_failed", "error", err)
				values = make(map[string]string)
				for _, key := range AdvancedSchedulerRuntimeSettingKeys() {
					if value, valueErr := repo.GetValue(dbCtx, key); valueErr == nil {
						values[key] = value
					}
				}
			}
			stickyWeightedEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyAdvancedSchedulerStickyWeightedEnabled]), "true")
			subscriptionPriorityEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled]), "true")
			lbTopKOverride = ParsePositiveIntOverride(values[SettingKeyAdvancedSchedulerLBTopK])
			weightOverrides = ParseAdvancedSchedulerWeightOverrides(values)
			ewmaErrorRateAlpha, ewmaErrorRateAlphaSet = ParseAdvancedSchedulerAlphaOverride(values[SettingKeyAdvancedSchedulerEWMAErrorRateAlpha], defaults.EwmaErrorRateAlpha)
			ewmaTTFTAlpha, ewmaTTFTAlphaSet = ParseAdvancedSchedulerAlphaOverride(values[SettingKeyAdvancedSchedulerEWMATTFTAlpha], defaults.EwmaTTFTAlpha)
			stickyEscapeEnabled, stickyEscapeEnabledSet = ParseAdvancedSchedulerBoolOverride(values[SettingKeyAdvancedSchedulerStickyEscapeEnabled], defaults.StickyEscape.Enabled)
			stickyEscapeTTFTMs, stickyEscapeTTFTMsSet = ParseAdvancedSchedulerPositiveFloatOverride(values[SettingKeyAdvancedSchedulerStickyEscapeTTFTMs], defaults.StickyEscape.TtftMs)
			stickyEscapeErrorRate, stickyEscapeErrorRateSet = ParseAdvancedSchedulerRateOverride(values[SettingKeyAdvancedSchedulerStickyEscapeErrorRate], defaults.StickyEscape.ErrorRate)
		}

		// Publish an isolated weight map because later request-specific overrides mutate
		// returned maps. Scalar settings and presence flags travel together in one cached value.
		s.cache.Store(&cachedAdvancedSchedulerSetting{
			StickyWeightedEnabled:       stickyWeightedEnabled,
			SubscriptionPriorityEnabled: subscriptionPriorityEnabled,
			LbTopKOverride:              lbTopKOverride,
			WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(weightOverrides),
			EwmaErrorRateAlpha:          ewmaErrorRateAlpha,
			EwmaErrorRateAlphaSet:       ewmaErrorRateAlphaSet,
			EwmaTTFTAlpha:               ewmaTTFTAlpha,
			EwmaTTFTAlphaSet:            ewmaTTFTAlphaSet,
			StickyEscapeEnabled:         stickyEscapeEnabled,
			StickyEscapeEnabledSet:      stickyEscapeEnabledSet,
			StickyEscapeTTFTMs:          stickyEscapeTTFTMs,
			StickyEscapeTTFTMsSet:       stickyEscapeTTFTMsSet,
			StickyEscapeErrorRate:       stickyEscapeErrorRate,
			StickyEscapeErrorRateSet:    stickyEscapeErrorRateSet,
			StickyEscape:                policy.StickyEscapeConfig{Enabled: stickyEscapeEnabled, TtftMs: stickyEscapeTTFTMs, ErrorRate: stickyEscapeErrorRate},
			expiresAt:                   time.Now().Add(advancedSchedulerSettingCacheTTL).UnixNano(),
		})
		return policy.RuntimeSettings{
			StickyWeightedEnabled:       stickyWeightedEnabled,
			SubscriptionPriorityEnabled: subscriptionPriorityEnabled,
			LbTopKOverride:              lbTopKOverride,
			WeightOverrides:             weightOverrides,
			EwmaErrorRateAlpha:          ewmaErrorRateAlpha,
			EwmaErrorRateAlphaSet:       ewmaErrorRateAlphaSet,
			EwmaTTFTAlpha:               ewmaTTFTAlpha,
			EwmaTTFTAlphaSet:            ewmaTTFTAlphaSet,
			StickyEscapeEnabled:         stickyEscapeEnabled,
			StickyEscapeEnabledSet:      stickyEscapeEnabledSet,
			StickyEscapeTTFTMs:          stickyEscapeTTFTMs,
			StickyEscapeTTFTMsSet:       stickyEscapeTTFTMsSet,
			StickyEscapeErrorRate:       stickyEscapeErrorRate,
			StickyEscapeErrorRateSet:    stickyEscapeErrorRateSet,
			StickyEscape:                policy.StickyEscapeConfig{Enabled: stickyEscapeEnabled, TtftMs: stickyEscapeTTFTMs, ErrorRate: stickyEscapeErrorRate},
		}, nil
	})

	settings, _ := result.(policy.RuntimeSettings)
	// Singleflight shares its result among waiting callers. Copy the weight map here
	// so one caller's group-specific changes cannot alter another caller's returned settings.
	settings.WeightOverrides = CloneAdvancedSchedulerWeightOverrides(settings.WeightOverrides)
	return settings
}

// ParseAdvancedSchedulerAlphaOverride returns a valid finite coefficient and
// its presence flag. Empty text or values outside zero exclusive to one
// inclusive return the fallback with false presence.
func ParseAdvancedSchedulerAlphaOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	// Coefficients outside this range cannot supply a valid EWMA blend. Mark the
	// override absent so effective feedback continues with its inherited
	// coefficient.
	if err != nil || value <= 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

// ParseAdvancedSchedulerBoolOverride returns a parsed boolean and
// successful-override presence. Empty or malformed text returns the supplied
// fallback with false presence, keeping an explicit parsed false
// distinguishable from inheritance.
func ParseAdvancedSchedulerBoolOverride(raw string, fallback bool) (bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseBool(raw)
	// Malformed switch text provides no administrative decision. Return false
	// presence so inherited escape policy remains distinguishable from a
	// successfully parsed disabled switch.
	if err != nil {
		return fallback, false
	}
	return value, true
}

// ParseAdvancedSchedulerPositiveFloatOverride returns a finite positive float
// and successful-override presence. Empty, malformed, nonpositive, NaN, or
// infinite text returns the fallback and a false presence flag.
func ParseAdvancedSchedulerPositiveFloatOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	// Escape latency thresholds compare positive millisecond values with observed
	// TTFT. Invalid threshold text leaves the inherited limit available to that
	// later comparison.
	if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

// ParseAdvancedSchedulerRateOverride returns a finite fraction between zero
// and one inclusive. Empty, malformed, or out-of-range text returns the
// supplied fallback with false presence; an explicit zero remains present.
func ParseAdvancedSchedulerRateOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	// Sticky escape compares an observed error fraction against this limit.
	// Reject unusable fractions before they become effective policy and keep the
	// inherited threshold.
	if err != nil || value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

// AdvancedSchedulerRuntimeSettingKeys returns a fresh slice in batch and
// recovery lookup order. Callers may change their returned slice; later calls
// still rebuild the complete key list from the fixed settings and weight
// specifications.
func AdvancedSchedulerRuntimeSettingKeys() []string {
	keys := []string{
		SettingKeyAdvancedSchedulerStickyWeightedEnabled,
		SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled,
		SettingKeyAdvancedSchedulerLBTopK,
		SettingKeyAdvancedSchedulerEWMAErrorRateAlpha,
		SettingKeyAdvancedSchedulerEWMATTFTAlpha,
		SettingKeyAdvancedSchedulerStickyEscapeEnabled,
		SettingKeyAdvancedSchedulerStickyEscapeTTFTMs,
		SettingKeyAdvancedSchedulerStickyEscapeErrorRate,
	}
	for _, spec := range AdvancedSchedulerWeightOverrideSpecs() {
		keys = append(keys, spec.Key)
	}
	return keys
}

// AdvancedSchedulerWeightOverrideSpecs returns the ordered settings-key
// associations for shared scoring weight names.
func AdvancedSchedulerWeightOverrideSpecs() []WeightOverrideSpec {
	return []WeightOverrideSpec{
		{Key: SettingKeyAdvancedSchedulerWeightPriority, Name: "priority"},
		{Key: SettingKeyAdvancedSchedulerWeightLoad, Name: "load"},
		{Key: SettingKeyAdvancedSchedulerWeightQueue, Name: "queue"},
		{Key: SettingKeyAdvancedSchedulerWeightErrorRate, Name: "error_rate"},
		{Key: SettingKeyAdvancedSchedulerWeightTTFT, Name: "ttft"},
		{Key: SettingKeyAdvancedSchedulerWeightReset, Name: "reset"},
		{Key: SettingKeyAdvancedSchedulerWeightQuotaHeadroom, Name: "quota_headroom"},
		{Key: SettingKeyAdvancedSchedulerWeightPreviousResponse, Name: "previous_response"},
		{Key: SettingKeyAdvancedSchedulerWeightSessionSticky, Name: "session_sticky"},
	}
}

// ParsePositiveIntOverride returns a positive integer override or zero for
// inheritance. It trims input and maps empty, malformed, or nonpositive text
// to zero so effective settings can select their default.
func ParsePositiveIntOverride(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	// Effective Top-K resolution treats zero as an absent override. Recover
	// malformed stored sizes to that sentinel so validated process configuration
	// can supply the sample size.
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// ParseAdvancedSchedulerWeightOverrides returns known finite, nonnegative
// weights keyed by scoring name. It trims stored text and omits empty or
// invalid entries, while preserving explicit zero weights for later policy
// resolution.
func ParseAdvancedSchedulerWeightOverrides(values map[string]string) map[string]float64 {
	overrides := map[string]float64{}
	for _, spec := range AdvancedSchedulerWeightOverrideSpecs() {
		raw := strings.TrimSpace(values[spec.Key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		// Invalid individual weight text contributes no override. Effective settings
		// can inherit that factor while retaining independently valid weights from the same read.
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		overrides[spec.Name] = value
	}
	return overrides
}

// CloneAdvancedSchedulerWeightOverrides copies weight-map entries into
// caller-owned storage. A nil or empty source returns nil, and scalar float
// values require no additional nested ownership handling.
func CloneAdvancedSchedulerWeightOverrides(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	maps.Copy(out, in)
	return out
}

// Store publishes prepared overrides with a copied weight map and a renewed
// five-second lifetime. The caller keeps the input map stable during this
// copy, then may modify its own storage independently.
//
// Forgetting the coalescing key permits a later expired load to start
// separately from any earlier read. It does not cancel that read or reserve
// publication order: an in-flight Load can replace the saved value afterward.
// Callers that require persistence to win over every concurrent read need
// coordination outside this cache.
func (s *SettingsRuntime) Store(value policy.RuntimeSettings) {
	// Detach later coalesced loads from the older call before publishing the
	// saved settings. Forget changes lookup membership, so older work can still
	// reach its own cache store.
	s.sf.Forget("advanced_scheduler_settings")
	s.cache.Store(&cachedAdvancedSchedulerSetting{
		StickyWeightedEnabled:       value.StickyWeightedEnabled,
		SubscriptionPriorityEnabled: value.SubscriptionPriorityEnabled,
		LbTopKOverride:              value.LbTopKOverride,
		WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(value.WeightOverrides),
		EwmaErrorRateAlpha:          value.EwmaErrorRateAlpha,
		EwmaErrorRateAlphaSet:       value.EwmaErrorRateAlphaSet,
		EwmaTTFTAlpha:               value.EwmaTTFTAlpha,
		EwmaTTFTAlphaSet:            value.EwmaTTFTAlphaSet,
		StickyEscapeEnabled:         value.StickyEscapeEnabled,
		StickyEscapeEnabledSet:      value.StickyEscapeEnabledSet,
		StickyEscapeTTFTMs:          value.StickyEscapeTTFTMs,
		StickyEscapeTTFTMsSet:       value.StickyEscapeTTFTMsSet,
		StickyEscapeErrorRate:       value.StickyEscapeErrorRate,
		StickyEscapeErrorRateSet:    value.StickyEscapeErrorRateSet,
		StickyEscape:                value.StickyEscape,
		expiresAt:                   time.Now().Add(advancedSchedulerSettingCacheTTL).UnixNano(),
	})
}
