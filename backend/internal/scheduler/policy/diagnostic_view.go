package policy

import (
	"time"
)

// Administrative scheduler diagnostics carry a provider's advanced groups and
// an optional detailed calculation. Score factors, effective settings,
// exclusion counts, and policy decisions remain separate so readers can
// distinguish eligibility from ranking and understand missing observations or
// forced affinity.

// AdvancedSchedulerScoreDiagnosticRequest supplies one group, optional model,
// and simulated affinity provider IDs.
type AdvancedSchedulerScoreDiagnosticRequest struct {
	GroupID int64 `json:"group_id"` // positive diagnostic group identifier
	// RequestedModel is trimmed by GetDetail and limited to 512 bytes. Empty text
	// omits model-specific eligibility context from the baseline calculation.
	// optional requested model text
	RequestedModel string `json:"requested_model,omitempty"`
	// StickyProviderID uses zero for absent session context and a positive ID for
	// simulated affinity. GetDetail rejects negative IDs before scoring the
	// selected group.
	// simulated session provider identifier
	StickyProviderID int64 `json:"sticky_provider_id,omitempty"`
	// PreviousResponseProviderID uses zero for absent previous-response context.
	// Diagnostic policy recognizes an eligible OpenAI provider before forcing or
	// weighting that affinity signal.
	// simulated previous-response provider identifier
	PreviousResponseProviderID int64 `json:"previous_response_provider_id,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticProvider contains provider identity and
// persistent status for administrative display.
type AdvancedSchedulerScoreDiagnosticProvider struct {
	ID       int64  `json:"id"`       // persistent provider record identifier
	Name     string `json:"name"`     // configured provider display name
	Platform string `json:"platform"` // upstream provider platform identifier
	Type     string `json:"type"`     // provider authentication kind identifier
	Status   string `json:"status"`   // persistent provider lifecycle status
}

// AdvancedSchedulerScoreDiagnosticGroup contains the persistent advanced
// group's identity and display name.
type AdvancedSchedulerScoreDiagnosticGroup struct {
	ID   int64  `json:"id"`   // persistent advanced group identifier
	Name string `json:"name"` // configured group display name
}

// AdvancedSchedulerScoreDiagnosticGroupSummary contains a group's eligibility
// and optional target score for tab display. Overview tabs use baseline
// scoring, while the selected detail tab reflects its supplied simulation
// context.
type AdvancedSchedulerScoreDiagnosticGroupSummary struct {
	// summarized group identity fields
	AdvancedSchedulerScoreDiagnosticGroup
	Eligible bool `json:"eligible"` // diagnosed candidate eligibility flag
	// FinalScore is present when the target belongs to the diagnosed eligible
	// pool. A filtered group summary leaves it nil and records filtered status.
	FinalScore *float64 `json:"final_score,omitempty"` // optional eligible target score
	Status     string   `json:"status"`                // eligible or filtered display state
}

// AdvancedSchedulerScoreDiagnosticResponse contains generated group summaries
// and an optional detailed scheduler explanation.
type AdvancedSchedulerScoreDiagnosticResponse struct {
	// diagnosed provider identity fields
	Provider AdvancedSchedulerScoreDiagnosticProvider `json:"provider"`
	// UTC response generation instant
	GeneratedAt time.Time `json:"generated_at"`
	// score formula version identifier
	CalculationVersion string `json:"calculation_version"`
	// ordered advanced group summaries
	Groups []AdvancedSchedulerScoreDiagnosticGroupSummary `json:"groups"`
	// Detail is populated for a requested advanced-group calculation. Overview
	// responses keep it nil and return summaries for all of the provider's
	// advanced groups.
	// optional selected group explanation
	Detail *AdvancedSchedulerScoreDiagnosticDetail `json:"detail,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticContext records the normalized model and
// simulated provider affinities used for calculation.
type AdvancedSchedulerScoreDiagnosticContext struct {
	// normalized requested model text
	RequestedModel string `json:"requested_model,omitempty"`
	// simulated session provider identifier
	StickyProviderID int64 `json:"sticky_provider_id,omitempty"`
	// simulated previous-response provider identifier
	PreviousResponseProviderID int64 `json:"previous_response_provider_id,omitempty"`
	// Baseline is true when model text and both affinity IDs are absent. It
	// describes the supplied scoring context independently of whether the
	// provider is eligible.
	Baseline bool `json:"baseline"` // absent simulation context flag
}

// AdvancedSchedulerScoreDiagnosticDetail separates one group's eligibility,
// candidate pool, scoring factors, and effective policy.
type AdvancedSchedulerScoreDiagnosticDetail struct {
	// selected advanced group identity
	Group AdvancedSchedulerScoreDiagnosticGroup `json:"group"`
	// calculation input context values
	Context AdvancedSchedulerScoreDiagnosticContext `json:"context"`
	// target scoring pool eligibility
	Eligible bool `json:"eligible"`
	// target exclusion reason identifiers
	HardFilterReasons []string `json:"hard_filter_reasons,omitempty"`
	// group candidate ranking explanation
	CandidatePool AdvancedSchedulerScoreDiagnosticCandidatePool `json:"candidate_pool"`
	// Score is populated after the target is found in the ranked eligible pool.
	// Filtered or deferred targets keep it nil and retain their exclusion
	// reasons.
	// optional eligible target calculation
	Score *AdvancedSchedulerScoreDiagnosticScore `json:"score,omitempty"`
	// target normalized factor explanations
	Metrics []AdvancedSchedulerScoreDiagnosticMetric `json:"metrics"`
	// resolved scheduler configuration values
	EffectiveSettings []AdvancedSchedulerScoreDiagnosticSetting `json:"effective_settings"`
	// separate policy outcome explanations
	PolicySignals []AdvancedSchedulerScoreDiagnosticPolicySignal `json:"policy_signals"`
}

// AdvancedSchedulerScoreDiagnosticCandidatePool contains candidate counts,
// normalization observations, and weighted Top-K membership.
type AdvancedSchedulerScoreDiagnosticCandidatePool struct {
	// eligible plus excluded count
	TotalCandidates int `json:"total_candidates"`
	// ranked scoring candidate count
	EligibleCandidates int `json:"eligible_candidates"`
	// summed exclusion reason counts
	ExcludedCandidates int `json:"excluded_candidates"`
	// candidate counts by rejection
	ExclusionReasons map[string]int `json:"exclusion_reasons"`
	// actual sampled pool size
	TopK int `json:"top_k"`
	// weighted pool minimum score
	// TopKMinimumScore and TopKWeightSum are present when the weighted pool is
	// nonempty. Both values describe that pool even when hard affinity overrides
	// the displayed probabilities.
	TopKMinimumScore *float64 `json:"top_k_minimum_score,omitempty"`
	// weighted pool total weight
	TopKWeightSum *float64 `json:"top_k_weight_sum,omitempty"`
	// observed factor range values
	NormalizationRanges AdvancedSchedulerScoreDiagnosticRanges `json:"normalization_ranges"`
	// ordered eligible provider descriptions
	Candidates []AdvancedSchedulerScoreDiagnosticCandidate `json:"candidates"`
}

// AdvancedSchedulerScoreDiagnosticRanges contains available pool extrema used
// to explain normalized scoring factors.
type AdvancedSchedulerScoreDiagnosticRanges struct {
	// PriorityMin and PriorityMax span the numeric priorities in the eligible
	// pool. Equal values give every candidate a full priority factor because the
	// pool supplies no priority distinction.
	PriorityMin     int `json:"priority_min"`      // smallest numeric provider priority
	PriorityMax     int `json:"priority_max"`      // largest numeric provider priority
	MaxWaitingCount int `json:"max_waiting_count"` // queue normalization denominator count
	// TTFTMinMs and TTFTMaxMs describe the pool's positive TTFT observations
	// together. Nil values mark absent observations so diagnostics can explain
	// the scorer's neutral latency factor.
	TTFTMinMs *float64 `json:"ttft_min_ms,omitempty"` // milliseconds
	TTFTMaxMs *float64 `json:"ttft_max_ms,omitempty"` // milliseconds
	// ResetMinSeconds and ResetMaxSeconds describe future reset observations when
	// reset scoring is enabled. Nil values mark an unavailable range and retain
	// neutral reset scoring.
	ResetMinSeconds *float64 `json:"reset_min_seconds,omitempty"` // seconds
	ResetMaxSeconds *float64 `json:"reset_max_seconds,omitempty"` // seconds
}

// AdvancedSchedulerScoreDiagnosticCandidate records one ranked provider with
// optional weighted-selection information.
type AdvancedSchedulerScoreDiagnosticCandidate struct {
	ID         int64   `json:"id"`          // persistent candidate provider identifier
	Name       string  `json:"name"`        // configured provider display name
	Platform   string  `json:"platform"`    // upstream provider platform identifier
	Priority   int     `json:"priority"`    // global numeric provider priority
	FinalScore float64 `json:"final_score"` // base score plus affinity
	Rank       int     `json:"rank"`        // one-based eligible pool rank
	InTopK     bool    `json:"in_top_k"`    // weighted sample membership flag
	// SelectionWeight is present for a provider in the weighted Top-K. A forced
	// provider outside that pool can have a probability while its weight remains
	// absent.
	// optional weighted sample weight
	SelectionWeight *float64 `json:"selection_weight,omitempty"`
	// SelectionProbability describes weighted first-choice probability or forced
	// affinity selection. Forced providers can receive one while retaining a rank
	// outside Top-K; ordinary candidates outside the sample keep it absent.
	// optional first-draw selection probability
	SelectionProbability *float64 `json:"selection_probability,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticScore contains a target's factor sum,
// affinity bonus, ranking, and selection policy.
type AdvancedSchedulerScoreDiagnosticScore struct {
	BaseScore   float64 `json:"base_score"`   // normalized weighted factor sum
	StickyBonus float64 `json:"sticky_bonus"` // combined enabled affinity bonuses
	FinalScore  float64 `json:"final_score"`  // base score plus affinity
	Rank        int     `json:"rank"`         // one-based eligible pool rank
	InTopK      bool    `json:"in_top_k"`     // weighted sample membership flag
	// SelectionWeight is present when the target has a Top-K sampling weight. A
	// forced affinity target outside the sample keeps this value absent.
	// optional weighted sample weight
	SelectionWeight *float64 `json:"selection_weight,omitempty"`
	// SelectionProbability follows the weighted sample or forced affinity
	// decision. A forced target can have probability one outside Top-K while its
	// rank and membership remain unchanged.
	// optional first-choice selection probability
	SelectionProbability *float64 `json:"selection_probability,omitempty"`
	SelectionMode        string   `json:"selection_mode"` // weighted or forced strategy
	Formula              string   `json:"formula"`        // formatted weighted calculation explanation
}

// AdvancedSchedulerScoreDiagnosticMetric connects one observed factor with its
// normalization and weighted contribution.
type AdvancedSchedulerScoreDiagnosticMetric struct {
	Key      string `json:"key"`       // scoring factor name identifier
	RawValue string `json:"raw_value"` // formatted source observation text
	// formatted factor calculation explanation
	Normalization        string  `json:"normalization"`
	NormalizedValue      float64 `json:"normalized_value"`      // scoring factor normalized value
	Weight               float64 `json:"weight"`                // resolved factor multiplier value
	WeightedContribution float64 `json:"weighted_contribution"` // factor multiplied by weight
	// Available and Neutral describe the diagnostic's treatment of an
	// observation. Equal-range TTFT can be available and neutral, while
	// unobserved error feedback receives full health. Quota headroom exactly 0.5
	// is marked unavailable and neutral, including a value supplied by the
	// platform adapter.
	Available bool   `json:"available"` // diagnosed observation availability flag
	Neutral   bool   `json:"neutral"`   // unobserved or neutral factor flag
	Source    string `json:"source"`    // factor observation source name
	// optional observation update instant
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticSetting records an effective scheduler value
// and its inferred configuration source.
type AdvancedSchedulerScoreDiagnosticSetting struct {
	Key    string `json:"key"`    // scheduler configuration field name
	Value  string `json:"value"`  // formatted effective setting value
	Source string `json:"source"` // inferred winning configuration layer
}

// AdvancedSchedulerScoreDiagnosticPolicySignal reports an eligibility or
// strategy decision outside the numeric score factors.
type AdvancedSchedulerScoreDiagnosticPolicySignal struct {
	Key    string `json:"key"`    // policy or eligibility name
	State  string `json:"state"`  // evaluated strategy outcome state
	Detail string `json:"detail"` // human-readable policy decision explanation
}
