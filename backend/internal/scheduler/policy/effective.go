package policy

import (
	"fmt"
	"math"
)

// effective.go resolves and validates effective scheduler policy from process
// defaults, global settings, and sparse group overrides.
// Advanced scheduling resolves each group field over dynamic global values and
// process defaults. Pointer presence preserves explicit false and zero group
// choices, including a deliberately zero base score. Global coefficients still
// require a positive base sum before they can supply the inherited weight set.
//
// ResolveEffective normalizes feedback and escape thresholds after applying
// the group values. Weight validation is separate from those switches and
// thresholds: an invalid merged weight set reverts to the global weights while
// other group choices remain effective. This prevents a malformed coefficient
// combination from discarding valid group policy. Missing, invalid, and wholly
// zero escape configuration also differ, so their normalization is described
// where it is applied.

// EWMA defaults normalize invalid coefficients, while the message-queue
// strings identify stored serialization and throttling choices read by request
// adapters.
const (
	defaultAdvancedSchedulerErrorRateAlpha = 0.2
	defaultAdvancedSchedulerTTFTAlpha      = 0.2

	// MessageQueueSerialize and MessageQueueThrottle are stored mode names. The
	// request adapter selects lock-based serialization or delay-based throttling from them.
	MessageQueueSerialize = "serialize" // provider serialization lock
	MessageQueueThrottle  = "throttle"  // RPM aware spacing
)

// EffectiveSettings contains request settings after global and group overrides are merged.
// Group fields take precedence; missing fields inherit runtime settings and process defaults.
type EffectiveSettings struct {
	StickyWeightedEnabled       bool               // weighted affinity switch
	SubscriptionPriorityEnabled bool               // subscription pool preference
	TopK                        int                // sampled candidate count
	Weights                     ScoreWeights       // resolved score coefficients
	Feedback                    FeedbackConfig     // resolved sample coefficients
	StickyEscape                StickyEscapeConfig // resolved escape thresholds
}

// ScoreWeights holds the coefficients for candidate factors and optional
// affinity bonuses.
type ScoreWeights struct {
	Priority      float64 // global priority coefficient
	Load          float64 // provider load coefficient
	Queue         float64 // waiting count coefficient
	ErrorRate     float64 // feedback health coefficient
	TTFT          float64 // token latency coefficient
	Reset         float64 // window reset coefficient
	QuotaHeadroom float64 // platform quota coefficient
	// scheduler.ScoreCandidates applies Previous and SessionSticky for
	// enabled weighted stickiness with a matching positive provider
	// identity. Validation includes both in the total sum even when this
	// request cannot use their bonuses.
	Previous      float64 // previous response bonus
	SessionSticky float64 // session provider bonus
}

// RuntimeSettings carries parsed dynamic values and presence flags for group
// resolution and diagnostics.
type RuntimeSettings struct {
	StickyWeightedEnabled       bool // weighted affinity switch
	SubscriptionPriorityEnabled bool // subscription pool preference
	// LbTopKOverride supplies a global candidate count when positive.
	// ResolveEffective inherits the process count for nonpositive values
	// and gives a positive group override precedence.
	LbTopKOverride int // positive candidate count override
	// WeightOverrides maps scoring names to supplied coefficients. Copying
	// RuntimeSettings shares this map, so callers that change its contents
	// need separately cloned storage.
	WeightOverrides map[string]float64 // named coefficient overrides
	// EwmaErrorRateAlpha and EwmaErrorRateAlphaSet preserve the parsed
	// coefficient and its presence. EwmaTTFTAlpha and EwmaTTFTAlphaSet do
	// the same for TTFT, allowing diagnostics to distinguish inherited
	// defaults from stored values.
	EwmaErrorRateAlpha    float64 // error sample coefficient
	EwmaErrorRateAlphaSet bool    // error coefficient presence
	EwmaTTFTAlpha         float64 // TTFT sample coefficient
	EwmaTTFTAlphaSet      bool    // TTFT coefficient presence
	// StickyEscapeEnabled and StickyEscapeEnabledSet, StickyEscapeTTFTMs
	// and StickyEscapeTTFTMsSet, and StickyEscapeErrorRate and
	// StickyEscapeErrorRateSet pair resolved values with dynamic-setting
	// presence. StickyEscape retains the parsed configuration used as
	// defaults by settings loading.
	StickyEscapeEnabled      bool               // health escape switch
	StickyEscapeEnabledSet   bool               // escape switch presence
	StickyEscapeTTFTMs       float64            // milliseconds
	StickyEscapeTTFTMsSet    bool               // latency threshold presence
	StickyEscapeErrorRate    float64            // error fraction threshold
	StickyEscapeErrorRateSet bool               // error threshold presence
	StickyEscape             StickyEscapeConfig // parsed escape defaults
}

// StickyEscapeConfig defines health thresholds used to escape a provider
// binding for one request.
type StickyEscapeConfig struct {
	// Enabled, TtftMs, and ErrorRate carry the escape switch with threshold
	// context. NormalizeStickyEscape enables a wholly zero struct; a false
	// switch accompanied by supplied thresholds remains disabled.
	Enabled bool // health escape switch
	// TtftMs supplies the millisecond latency threshold.
	// NormalizeStickyEscape substitutes its scheduler default for
	// nonpositive or nonfinite values before the threshold is used.
	TtftMs float64 // milliseconds
	// ErrorRate is the error fraction compared by
	// scheduler.ShouldEscapeSticky using a strict greater-than test.
	// NormalizeStickyEscape replaces invalid fractions and the wholly unset
	// threshold pair with its default.
	ErrorRate float64 // error fraction threshold
}

// FeedbackConfig contains the EWMA coefficients used when a request records feedback.
// Statistics are shared per provider; the final request group determines these coefficients.
type FeedbackConfig struct {
	// ErrorRateAlpha and TtftAlpha weight new error and latency samples
	// independently. NormalizeFeedback replaces nonfinite values and
	// coefficients outside (0, 1] with scheduler defaults.
	ErrorRateAlpha float64 // error sample coefficient
	TtftAlpha      float64 // TTFT sample coefficient
}

// ValidateGroupOverrides validates each supplied group value without resolving
// inherited fields. Explicit false and zero weights remain valid, and this
// check permits a wholly zero base contribution; merged sum validation is
// performed separately.
func ValidateGroupOverrides(overrides GroupAdvancedSchedulerOverrides) error {
	if overrides.LBTopK != nil && *overrides.LBTopK <= 0 {
		return fmt.Errorf("lb_top_k must be a positive integer")
	}
	for _, item := range []struct {
		name  string
		value *float64
	}{{"ewma_error_rate_alpha", overrides.EWMAErrorRateAlpha}, {"ewma_ttft_alpha", overrides.EWMATTFTAlpha}} {
		if item.value == nil {
			continue
		}
		if *item.value <= 0 || *item.value > 1 || math.IsNaN(*item.value) || math.IsInf(*item.value, 0) {
			return fmt.Errorf("%s must be between 0 and 1", item.name)
		}
	}
	if overrides.StickyEscapeTTFTMs != nil && *overrides.StickyEscapeTTFTMs <= 0 {
		return fmt.Errorf("sticky_escape_ttft_ms must be positive")
	}
	if overrides.StickyEscapeErrorRate != nil &&
		(*overrides.StickyEscapeErrorRate < 0 || *overrides.StickyEscapeErrorRate > 1 ||
			math.IsNaN(*overrides.StickyEscapeErrorRate) || math.IsInf(*overrides.StickyEscapeErrorRate, 0)) {
		return fmt.Errorf("sticky_escape_error_rate must be between 0 and 1")
	}

	Weights := []struct {
		name  string
		value *float64
	}{
		{"weight_priority", overrides.WeightPriority},
		{"weight_load", overrides.WeightLoad},
		{"weight_queue", overrides.WeightQueue},
		{"weight_error_rate", overrides.WeightErrorRate},
		{"weight_ttft", overrides.WeightTTFT},
		{"weight_reset", overrides.WeightReset},
		{"weight_quota_headroom", overrides.WeightQuotaHeadroom},
		{"weight_previous_response", overrides.WeightPreviousResponse},
		{"weight_session_sticky", overrides.WeightSessionSticky},
	}
	for _, item := range Weights {
		if item.value == nil {
			continue
		}
		if *item.value < 0 || math.IsNaN(*item.value) || math.IsInf(*item.value, 0) {
			return fmt.Errorf("%s must be a non-negative finite number", item.name)
		}
	}

	return nil
}

// ValidateEffectiveWeights rejects negative or nonfinite coefficients and
// overflowed combined sums. A zero base is valid for group policy, including a
// score determined solely by affinity bonuses.
//
// The base and total sums are evaluated in scoring field order. Finite
// individual coefficients can overflow when combined, so checking fields alone
// cannot establish that the merged weights are usable. Callers resolve
// inheritance before applying this validation and decide how to handle its
// error.
func ValidateEffectiveWeights(weights ScoreWeights) error {
	values := []struct {
		name  string
		value float64
	}{
		{"weight_priority", weights.Priority},
		{"weight_load", weights.Load},
		{"weight_queue", weights.Queue},
		{"weight_error_rate", weights.ErrorRate},
		{"weight_ttft", weights.TTFT},
		{"weight_reset", weights.Reset},
		{"weight_quota_headroom", weights.QuotaHeadroom},
		{"weight_previous_response", weights.Previous},
		{"weight_session_sticky", weights.SessionSticky},
	}
	for _, item := range values {
		if item.value < 0 || math.IsNaN(item.value) || math.IsInf(item.value, 0) {
			return fmt.Errorf("%s must be a non-negative finite number", item.name)
		}
	}

	resolved := weights
	if baseSum := resolved.BaseWeightSum(); math.IsNaN(baseSum) || math.IsInf(baseSum, 0) {
		return fmt.Errorf("base-weight sum must be finite")
	}
	if totalSum := resolved.TotalWeightSum(); math.IsNaN(totalSum) || math.IsInf(totalSum, 0) {
		return fmt.Errorf("total-weight sum must be finite")
	}
	return nil
}

// HasWeightOverrides reports whether the group supplies at least one scoring
// coefficient.
func HasWeightOverrides(overrides GroupAdvancedSchedulerOverrides) bool {
	return overrides.WeightPriority != nil ||
		overrides.WeightLoad != nil ||
		overrides.WeightQueue != nil ||
		overrides.WeightErrorRate != nil ||
		overrides.WeightTTFT != nil ||
		overrides.WeightReset != nil ||
		overrides.WeightQuotaHeadroom != nil ||
		overrides.WeightPreviousResponse != nil ||
		overrides.WeightSessionSticky != nil
}

// ApplyGroupWeightOverrides replaces each coefficient supplied by a non-nil
// group pointer. It returns the changed value copy, retains inherited
// coefficients for missing fields, and accepts explicit zero without
// performing validation.
func ApplyGroupWeightOverrides(
	weights ScoreWeights,
	overrides GroupAdvancedSchedulerOverrides,
) ScoreWeights {
	if overrides.WeightPriority != nil {
		weights.Priority = *overrides.WeightPriority
	}
	if overrides.WeightLoad != nil {
		weights.Load = *overrides.WeightLoad
	}
	if overrides.WeightQueue != nil {
		weights.Queue = *overrides.WeightQueue
	}
	if overrides.WeightErrorRate != nil {
		weights.ErrorRate = *overrides.WeightErrorRate
	}
	if overrides.WeightTTFT != nil {
		weights.TTFT = *overrides.WeightTTFT
	}
	if overrides.WeightReset != nil {
		weights.Reset = *overrides.WeightReset
	}
	if overrides.WeightQuotaHeadroom != nil {
		weights.QuotaHeadroom = *overrides.WeightQuotaHeadroom
	}
	if overrides.WeightPreviousResponse != nil {
		weights.Previous = *overrides.WeightPreviousResponse
	}
	if overrides.WeightSessionSticky != nil {
		weights.SessionSticky = *overrides.WeightSessionSticky
	}
	return weights
}

// ResolveEffective returns request parameters after process, dynamic, and
// group values are combined. Nonpositive process TopK uses seven, positive
// dynamic TopK overrides it, and a positive supplied group count wins last.
//
// Group pointers preserve false and zero choices independently. Feedback and
// escape thresholds are normalized after group application. Global weight
// overrides must have a valid positive base or the process weight set is used;
// invalid merged group weights revert to that effective global set while the
// other group choices remain applied.
func ResolveEffective(
	baseTopK int,
	baseWeights ScoreWeights,
	global RuntimeSettings,
	overrides GroupAdvancedSchedulerOverrides,
) EffectiveSettings {
	if baseTopK <= 0 {
		baseTopK = 7
	}
	globalTopK := baseTopK
	if global.LbTopKOverride > 0 {
		globalTopK = global.LbTopKOverride
	}
	globalWeights := ApplyGlobalWeightOverrides(baseWeights, global.WeightOverrides)
	if !globalWeights.ValidGlobal() {
		// Dynamic coefficients cannot supply valid global scoring
		// weights. Use the process baseline so later group merging
		// starts with the caller-configured weight set.
		globalWeights = baseWeights
	}

	effective := EffectiveSettings{
		StickyWeightedEnabled:       global.StickyWeightedEnabled,
		SubscriptionPriorityEnabled: global.SubscriptionPriorityEnabled,
		TopK:                        globalTopK,
		Weights:                     globalWeights,
		Feedback:                    NormalizeFeedback(FeedbackConfig{ErrorRateAlpha: global.EwmaErrorRateAlpha, TtftAlpha: global.EwmaTTFTAlpha}),
		StickyEscape:                NormalizeStickyEscape(StickyEscapeConfig{Enabled: global.StickyEscapeEnabled, TtftMs: global.StickyEscapeTTFTMs, ErrorRate: global.StickyEscapeErrorRate}),
	}
	if overrides.StickyWeightedEnabled != nil {
		effective.StickyWeightedEnabled = *overrides.StickyWeightedEnabled
	}
	if overrides.SubscriptionPriorityEnabled != nil {
		effective.SubscriptionPriorityEnabled = *overrides.SubscriptionPriorityEnabled
	}
	if overrides.LBTopK != nil && *overrides.LBTopK > 0 {
		effective.TopK = *overrides.LBTopK
	}
	if overrides.EWMAErrorRateAlpha != nil {
		effective.Feedback.ErrorRateAlpha = *overrides.EWMAErrorRateAlpha
	}
	if overrides.EWMATTFTAlpha != nil {
		effective.Feedback.TtftAlpha = *overrides.EWMATTFTAlpha
	}
	if overrides.StickyEscapeEnabled != nil {
		effective.StickyEscape.Enabled = *overrides.StickyEscapeEnabled
	}
	if overrides.StickyEscapeTTFTMs != nil {
		// Convert integer milliseconds to the floating threshold used
		// by feedback.
		effective.StickyEscape.TtftMs = float64(*overrides.StickyEscapeTTFTMs)
	}
	if overrides.StickyEscapeErrorRate != nil {
		effective.StickyEscape.ErrorRate = *overrides.StickyEscapeErrorRate
	}
	effective.Feedback = NormalizeFeedback(effective.Feedback)
	effective.StickyEscape = NormalizeStickyEscape(effective.StickyEscape)
	effective.Weights = ApplyGroupWeightOverrides(effective.Weights, overrides)
	if validateErr := ValidateEffectiveWeights(effective.Weights); validateErr != nil {
		// The merged coefficients failed sum or field validation.
		// Retain the effective global weights while leaving
		// independently valid group switches and thresholds applied.
		effective.Weights = globalWeights
	}
	return effective
}

// NormalizeStickyEscape returns an escape policy with usable threshold values.
// A wholly zero configuration enables escape and supplies the default error
// fraction, distinguishing missing configuration from a disabled policy with
// supplied thresholds.
//
// Nonpositive or nonfinite TTFT uses fifteen thousand milliseconds. Invalid
// error fractions use one half, while an explicitly zero fraction is retained
// when at least one threshold was supplied. The Enabled value is otherwise
// retained, so callers represent a deliberate disabled policy with threshold
// context.
func NormalizeStickyEscape(value StickyEscapeConfig) StickyEscapeConfig {
	thresholdsUnset := value.TtftMs == 0 && value.ErrorRate == 0
	if !value.Enabled && value.TtftMs == 0 && value.ErrorRate == 0 {
		// With no threshold context, the zero struct represents
		// missing configuration. Enable health escape here so an
		// unconfigured policy receives the scheduler defaults.
		value.Enabled = true
	}
	if value.TtftMs <= 0 || math.IsNaN(value.TtftMs) || math.IsInf(value.TtftMs, 0) {
		value.TtftMs = 15000
	}
	if value.ErrorRate < 0 || value.ErrorRate > 1 || math.IsNaN(value.ErrorRate) || math.IsInf(value.ErrorRate, 0) {
		value.ErrorRate = 0.5
	}
	if thresholdsUnset {
		value.ErrorRate = 0.5
	}
	return value
}

// ApplyGlobalWeightOverrides replaces recognized scoring coefficients in the
// supplied value copy. Unknown keys are ignored, explicit zero is applied, and
// callers validate individual values and combined sums after merging.
func ApplyGlobalWeightOverrides(
	weights ScoreWeights,
	overrides map[string]float64,
) ScoreWeights {
	for key, value := range overrides {
		switch key {
		case "priority":
			weights.Priority = value
		case "load":
			weights.Load = value
		case "queue":
			weights.Queue = value
		case "error_rate":
			weights.ErrorRate = value
		case "ttft":
			weights.TTFT = value
		case "reset":
			weights.Reset = value
		case "quota_headroom":
			weights.QuotaHeadroom = value
		case "previous_response":
			weights.Previous = value
		case "session_sticky":
			weights.SessionSticky = value
		}
	}
	return weights
}

// NormalizeFeedback supplies default EWMA coefficients for invalid values
// independently. Each valid coefficient lies above zero and at most one;
// callers can configure error and latency samples with different weights.
func NormalizeFeedback(value FeedbackConfig) FeedbackConfig {
	if value.ErrorRateAlpha <= 0 || value.ErrorRateAlpha > 1 || math.IsNaN(value.ErrorRateAlpha) || math.IsInf(value.ErrorRateAlpha, 0) {
		value.ErrorRateAlpha = defaultAdvancedSchedulerErrorRateAlpha
	}
	if value.TtftAlpha <= 0 || value.TtftAlpha > 1 || math.IsNaN(value.TtftAlpha) || math.IsInf(value.TtftAlpha, 0) {
		value.TtftAlpha = defaultAdvancedSchedulerTTFTAlpha
	}
	return value
}

// BaseWeightSum adds ordinary factor coefficients in the order used by
// scoring.
func (w ScoreWeights) BaseWeightSum() float64 {
	return w.Priority + w.Load + w.Queue + w.ErrorRate + w.TTFT + w.Reset + w.QuotaHeadroom
}

// TotalWeightSum adds the base coefficients and both optional provider
// affinity bonuses.
func (w ScoreWeights) TotalWeightSum() float64 {
	return w.BaseWeightSum() + w.Previous + w.SessionSticky
}

// ValidGlobal reports whether the weights are finite, nonnegative global
// defaults with a positive base sum. Group policies use
// ValidateEffectiveWeights directly so their deliberate zero base remains
// valid.
func (w ScoreWeights) ValidGlobal() bool {
	return ValidateEffectiveWeights(w) == nil && w.BaseWeightSum() > 0
}
