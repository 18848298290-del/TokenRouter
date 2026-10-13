package scheduler

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Administrative scheduler input keeps numeric overrides as text so an empty
// value can inherit a process default. Normalization validates each supplied
// value, then resolves the weight set to reject combinations whose base or
// complete sum is unusable. Successful preparation stores the normalized text,
// preserving the distinction between inheritance and an explicit zero.
//
// The escape switch has a separate presence flag because false can either be
// supplied or left unset. The two other switches are ordinary booleans.
// ParticipantFields carries this distinction into partial settings updates,
// while PrepareAdminSettings uses it to select a stored boolean or an empty
// inheriting value. Callers perform persistence after preparation succeeds.

// AdminDefaults supplies process values used when administrative numeric
// overrides are empty.
type AdminDefaults struct {
	TopK    int                       // configured candidate sample size
	Weights policy.ConfigScoreWeights // process scoring factor weights
	Process policy.RuntimeSettings    // process feedback and escape defaults
}

// AdminSettings holds administrative scheduler values before normalization and
// persistent settings preparation. Numeric text uses empty strings for
// inheritance; the escape switch's presence flag distinguishes a supplied
// false from an omitted switch.
type AdminSettings struct {
	// error sample blending coefficient
	AdvancedSchedulerEWMAErrorRateAlpha string `json:"advanced_scheduler_ewma_error_rate_alpha"`
	// TTFT sample blending coefficient
	AdvancedSchedulerEWMATTFTAlpha string `json:"advanced_scheduler_ewma_ttft_alpha"`
	// candidate sample size override
	AdvancedSchedulerLBTopK string `json:"advanced_scheduler_lb_top_k"`
	// configured sticky escape switch
	AdvancedSchedulerStickyEscapeEnabled bool `json:"advanced_scheduler_sticky_escape_enabled"`
	// AdvancedSchedulerStickyEscapeEnabledSet records whether the escape switch
	// was supplied. Preparation stores an empty inheriting value when this flag
	// is false, and ParticipantFields omits that JSON field.
	AdvancedSchedulerStickyEscapeEnabledSet bool `json:"-"` // escape switch input presence
	// escape error fraction threshold
	AdvancedSchedulerStickyEscapeErrorRate string `json:"advanced_scheduler_sticky_escape_error_rate"`
	// escape TTFT threshold milliseconds
	AdvancedSchedulerStickyEscapeTTFTMs string `json:"advanced_scheduler_sticky_escape_ttft_ms"`
	// weighted session affinity switch
	AdvancedSchedulerStickyWeightedEnabled bool `json:"advanced_scheduler_sticky_weighted_enabled"`
	// subscription candidate preference switch
	AdvancedSchedulerSubscriptionPriorityEnabled bool `json:"advanced_scheduler_subscription_priority_enabled"`
	// observed error health weight
	AdvancedSchedulerWeightErrorRate string `json:"advanced_scheduler_weight_error_rate"`
	// provider load factor weight
	AdvancedSchedulerWeightLoad string `json:"advanced_scheduler_weight_load"`
	// previous response affinity weight
	AdvancedSchedulerWeightPreviousResponse string `json:"advanced_scheduler_weight_previous_response"`
	// global provider priority weight
	AdvancedSchedulerWeightPriority string `json:"advanced_scheduler_weight_priority"`
	// provider waiting count weight
	AdvancedSchedulerWeightQueue string `json:"advanced_scheduler_weight_queue"`
	// remaining platform quota weight
	AdvancedSchedulerWeightQuotaHeadroom string `json:"advanced_scheduler_weight_quota_headroom"`
	// upcoming window reset weight
	AdvancedSchedulerWeightReset string `json:"advanced_scheduler_weight_reset"`
	// session binding affinity weight
	AdvancedSchedulerWeightSessionSticky string `json:"advanced_scheduler_weight_session_sticky"`
	// observed TTFT factor weight
	AdvancedSchedulerWeightTTFT string `json:"advanced_scheduler_weight_ttft"`
}

// DefaultAdminSettingsDefaults returns the management defaults used when
// process configuration is absent. The returned weight set is valid for global
// administrative validation, and the escape policy enables the default latency
// and error thresholds.
func DefaultAdminSettingsDefaults() AdminDefaults {
	return AdminDefaults{TopK: 7, Weights: policy.ConfigScoreWeights{Priority: 1, Load: 1, Queue: 0.7, ErrorRate: 0.8, TTFT: 0.5, Reset: 0, QuotaHeadroom: 0, PreviousResponse: 5, SessionSticky: 3}, Process: policy.RuntimeSettings{EwmaErrorRateAlpha: DefaultErrorRateAlpha, EwmaTTFTAlpha: DefaultTTFTAlpha, StickyEscape: policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: true, TtftMs: 15000, ErrorRate: 0.5})}}
}

// EffectiveAdminWeights returns valid process weights or the management
// default weight set. Invalid individual values or unusable combined sums
// trigger replacement of the complete set before administrative overrides are
// resolved.
func EffectiveAdminWeights(defaults AdminDefaults) policy.ConfigScoreWeights {
	if defaults.Weights.IsValid() {
		return defaults.Weights
	}
	// Validation resolves partial overrides against one valid base set. Replace the
	// complete invalid process set before individual administrative values can inherit it.
	return DefaultAdminSettingsDefaults().Weights
}

// EffectiveAdminTopK formats the process sample size for administrative
// output. A nonpositive process value produces the management default of
// seven, matching the default returned when process configuration is absent.
func EffectiveAdminTopK(defaults AdminDefaults) string {
	if defaults.TopK > 0 {
		return strconv.Itoa(defaults.TopK)
	}
	return "7"
}

// NormalizeAdminSettings canonicalizes supplied numeric text and validates the
// resolved global weight set. Callers provide a nonnil settings pointer;
// normalization mutates earlier fields even when a later field fails
// validation, so an error does not mean the input object is untouched.
//
// Empty numeric fields inherit validated process values, while an explicit
// zero remains a weight override. Individual finite, nonnegative weights can
// still overflow when added together, so both resolved sums are checked after
// the per-field passes. Global settings require nonzero base and total sums,
// unlike the separate group override policy that permits a zero base.
func NormalizeAdminSettings(settings *AdminSettings, defaults AdminDefaults) error {
	lbTopK, err := NormalizeOptionalPositiveIntString(settings.AdvancedSchedulerLBTopK)
	if err != nil {
		return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_LB_TOP_K", "advanced scheduler TopK must be a positive integer or empty")
	}
	settings.AdvancedSchedulerLBTopK = lbTopK
	for _, item := range []struct {
		name   string  // administrative validation error label
		target *string // supplied coefficient text destination
	}{
		{"advanced scheduler error-rate EWMA alpha", &settings.AdvancedSchedulerEWMAErrorRateAlpha},
		{"advanced scheduler TTFT EWMA alpha", &settings.AdvancedSchedulerEWMATTFTAlpha},
	} {
		normalized, err := NormalizeOptionalAlphaString(*item.target)
		if err != nil {
			return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_EWMA_ALPHA", item.name+" must be greater than 0 and at most 1, or empty")
		}
		*item.target = normalized
	}
	stickyTTFT, err := NormalizeOptionalPositiveIntString(settings.AdvancedSchedulerStickyEscapeTTFTMs)
	if err != nil {
		return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_STICKY_ESCAPE_TTFT", "advanced scheduler sticky escape TTFT must be a positive integer or empty")
	}
	settings.AdvancedSchedulerStickyEscapeTTFTMs = stickyTTFT
	stickyRate, err := NormalizeOptionalRateString(settings.AdvancedSchedulerStickyEscapeErrorRate)
	if err != nil {
		return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_STICKY_ESCAPE_ERROR_RATE", "advanced scheduler sticky escape error rate must be between 0 and 1, or empty")
	}
	settings.AdvancedSchedulerStickyEscapeErrorRate = stickyRate

	weights := []*string{
		&settings.AdvancedSchedulerWeightPriority,
		&settings.AdvancedSchedulerWeightLoad,
		&settings.AdvancedSchedulerWeightQueue,
		&settings.AdvancedSchedulerWeightErrorRate,
		&settings.AdvancedSchedulerWeightTTFT,
		&settings.AdvancedSchedulerWeightReset,
		&settings.AdvancedSchedulerWeightQuotaHeadroom,
		&settings.AdvancedSchedulerWeightPreviousResponse,
		&settings.AdvancedSchedulerWeightSessionSticky,
	}
	for _, target := range weights {
		normalized, err := NormalizeOptionalNonNegativeFloatString(*target)
		if err != nil {
			return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_WEIGHT", "advanced scheduler weights must be non-negative numbers or empty")
		}
		*target = normalized
	}

	// Individually valid weights can overflow when combined. Resolve inherited values
	// before checking both sums so a partial administrative update cannot hide that failure.

	effective := EffectiveAdminWeights(defaults)
	resolved := policy.ConfigScoreWeights{
		Priority:         ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightPriority, effective.Priority),
		Load:             ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightLoad, effective.Load),
		Queue:            ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightQueue, effective.Queue),
		ErrorRate:        ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightErrorRate, effective.ErrorRate),
		TTFT:             ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightTTFT, effective.TTFT),
		Reset:            ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightReset, effective.Reset),
		QuotaHeadroom:    ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightQuotaHeadroom, effective.QuotaHeadroom),
		PreviousResponse: ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightPreviousResponse, effective.PreviousResponse),
		SessionSticky:    ResolveAdvancedSchedulerWeight(settings.AdvancedSchedulerWeightSessionSticky, effective.SessionSticky),
	}
	if !resolved.IsValid() {
		return apperror.BadRequest("INVALID_ADVANCED_SCHEDULER_WEIGHT", "advanced scheduler weights must have finite non-zero base and total sums")
	}
	return nil
}

// ResolveAdvancedSchedulerWeight parses normalized weight text or returns its
// fallback. Empty or unparsable text inherits the fallback; callers normalize
// and validate ranges separately because this helper accepts any successfully
// parsed float.
func ResolveAdvancedSchedulerWeight(normalized string, fallback float64) float64 {
	if normalized == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(normalized, 64)
	// A parse failure leaves the chosen process weight in place. Range validation
	// belongs to normalization, so this recovery handles malformed text alone.
	if err != nil {
		return fallback
	}
	return value
}

// NormalizeOptionalPositiveIntString trims text and canonicalizes a positive
// decimal integer. Empty input remains empty for inheritance, while a parse
// failure or nonpositive value returns an error.
func NormalizeOptionalPositiveIntString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return "", fmt.Errorf("invalid positive integer")
	}
	return strconv.Itoa(value), nil
}

// NormalizeOptionalNonNegativeFloatString canonicalizes finite, nonnegative
// float text without exponent notation. Empty input represents inheritance,
// and invalid, negative, NaN, or infinite values return an error.
func NormalizeOptionalNonNegativeFloatString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("invalid non-negative float")
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

// NormalizeOptionalAlphaString canonicalizes a finite EWMA coefficient in the
// range zero exclusive to one inclusive. Empty input remains an inheritance
// value, and values outside that range return an error.
func NormalizeOptionalAlphaString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("invalid alpha")
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

// NormalizeOptionalRateString canonicalizes a finite fraction between zero and
// one inclusive. Empty input remains an inheritance value, while invalid or
// out-of-range values return an error.
func NormalizeOptionalRateString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("invalid rate")
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

// PrepareAdminSettings normalizes the supplied object and returns scheduler
// key/value updates for persistence. Validation errors leave no update map,
// but earlier normalization may already have mutated fields; an absent escape
// switch becomes an empty stored value.
func PrepareAdminSettings(settings *AdminSettings, defaults AdminDefaults) (map[string]string, error) {
	if err := NormalizeAdminSettings(settings, defaults); err != nil {
		return nil, err
	}
	updates := map[string]string{}
	updates[SettingKeyAdvancedSchedulerStickyWeightedEnabled] = strconv.FormatBool(settings.AdvancedSchedulerStickyWeightedEnabled)
	updates[SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled] = strconv.FormatBool(settings.AdvancedSchedulerSubscriptionPriorityEnabled)
	updates[SettingKeyAdvancedSchedulerEWMAErrorRateAlpha] = settings.AdvancedSchedulerEWMAErrorRateAlpha
	updates[SettingKeyAdvancedSchedulerEWMATTFTAlpha] = settings.AdvancedSchedulerEWMATTFTAlpha
	if settings.AdvancedSchedulerStickyEscapeEnabledSet {
		updates[SettingKeyAdvancedSchedulerStickyEscapeEnabled] = strconv.FormatBool(settings.AdvancedSchedulerStickyEscapeEnabled)
	} else {
		updates[SettingKeyAdvancedSchedulerStickyEscapeEnabled] = ""
	}
	updates[SettingKeyAdvancedSchedulerStickyEscapeTTFTMs] = settings.AdvancedSchedulerStickyEscapeTTFTMs
	updates[SettingKeyAdvancedSchedulerStickyEscapeErrorRate] = settings.AdvancedSchedulerStickyEscapeErrorRate
	updates[SettingKeyAdvancedSchedulerLBTopK] = settings.AdvancedSchedulerLBTopK
	updates[SettingKeyAdvancedSchedulerWeightPriority] = settings.AdvancedSchedulerWeightPriority
	updates[SettingKeyAdvancedSchedulerWeightLoad] = settings.AdvancedSchedulerWeightLoad
	updates[SettingKeyAdvancedSchedulerWeightQueue] = settings.AdvancedSchedulerWeightQueue
	updates[SettingKeyAdvancedSchedulerWeightErrorRate] = settings.AdvancedSchedulerWeightErrorRate
	updates[SettingKeyAdvancedSchedulerWeightTTFT] = settings.AdvancedSchedulerWeightTTFT
	updates[SettingKeyAdvancedSchedulerWeightReset] = settings.AdvancedSchedulerWeightReset
	updates[SettingKeyAdvancedSchedulerWeightQuotaHeadroom] = settings.AdvancedSchedulerWeightQuotaHeadroom
	updates[SettingKeyAdvancedSchedulerWeightPreviousResponse] = settings.AdvancedSchedulerWeightPreviousResponse
	updates[SettingKeyAdvancedSchedulerWeightSessionSticky] = settings.AdvancedSchedulerWeightSessionSticky
	return updates, nil
}

// ParticipantFields encodes scheduler input into independent JSON field bytes
// for settings participants. It omits the escape switch when its presence flag
// is false, preserving omission through the typed boolean's otherwise
// unavoidable zero value.
func (value AdminSettings) ParticipantFields() (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if !value.AdvancedSchedulerStickyEscapeEnabledSet {
		delete(fields, SettingKeyAdvancedSchedulerStickyEscapeEnabled)
	}
	return fields, nil
}
