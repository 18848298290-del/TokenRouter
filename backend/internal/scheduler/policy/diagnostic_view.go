package policy

import (
	"time"
)

// AdvancedSchedulerScoreDiagnosticRequest identifies the group, model, and sticky providers to simulate.
type AdvancedSchedulerScoreDiagnosticRequest struct {
	GroupID                    int64  `json:"group_id"`
	RequestedModel             string `json:"requested_model,omitempty"`
	StickyProviderID           int64  `json:"sticky_provider_id,omitempty"`
	PreviousResponseProviderID int64  `json:"previous_response_provider_id,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticProvider summarizes provider identity and status for diagnostics.
type AdvancedSchedulerScoreDiagnosticProvider struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}

// AdvancedSchedulerScoreDiagnosticGroup identifies an advanced scheduler group.
type AdvancedSchedulerScoreDiagnosticGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// AdvancedSchedulerScoreDiagnosticGroupSummary supplies initial group tab eligibility and score data.
type AdvancedSchedulerScoreDiagnosticGroupSummary struct {
	AdvancedSchedulerScoreDiagnosticGroup
	Eligible   bool     `json:"eligible"`
	FinalScore *float64 `json:"final_score,omitempty"`
	Status     string   `json:"status"`
}

// AdvancedSchedulerScoreDiagnosticResponse is the admin score diagnostic response.
type AdvancedSchedulerScoreDiagnosticResponse struct {
	Provider           AdvancedSchedulerScoreDiagnosticProvider       `json:"provider"`
	GeneratedAt        time.Time                                      `json:"generated_at"`
	CalculationVersion string                                         `json:"calculation_version"`
	Groups             []AdvancedSchedulerScoreDiagnosticGroupSummary `json:"groups"`
	Detail             *AdvancedSchedulerScoreDiagnosticDetail        `json:"detail,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticContext contains the model and provider IDs used for scoring.
type AdvancedSchedulerScoreDiagnosticContext struct {
	RequestedModel             string `json:"requested_model,omitempty"`
	StickyProviderID           int64  `json:"sticky_provider_id,omitempty"`
	PreviousResponseProviderID int64  `json:"previous_response_provider_id,omitempty"`
	Baseline                   bool   `json:"baseline"`
}

// AdvancedSchedulerScoreDiagnosticDetail explains one group's score.
type AdvancedSchedulerScoreDiagnosticDetail struct {
	Group             AdvancedSchedulerScoreDiagnosticGroup          `json:"group"`
	Context           AdvancedSchedulerScoreDiagnosticContext        `json:"context"`
	Eligible          bool                                           `json:"eligible"`
	HardFilterReasons []string                                       `json:"hard_filter_reasons,omitempty"`
	CandidatePool     AdvancedSchedulerScoreDiagnosticCandidatePool  `json:"candidate_pool"`
	Score             *AdvancedSchedulerScoreDiagnosticScore         `json:"score,omitempty"`
	Metrics           []AdvancedSchedulerScoreDiagnosticMetric       `json:"metrics"`
	EffectiveSettings []AdvancedSchedulerScoreDiagnosticSetting      `json:"effective_settings"`
	PolicySignals     []AdvancedSchedulerScoreDiagnosticPolicySignal `json:"policy_signals"`
}

// AdvancedSchedulerScoreDiagnosticCandidatePool summarizes eligible candidates and Top-K selection data.
type AdvancedSchedulerScoreDiagnosticCandidatePool struct {
	TotalCandidates     int                                         `json:"total_candidates"`
	EligibleCandidates  int                                         `json:"eligible_candidates"`
	ExcludedCandidates  int                                         `json:"excluded_candidates"`
	ExclusionReasons    map[string]int                              `json:"exclusion_reasons"`
	TopK                int                                         `json:"top_k"`
	TopKMinimumScore    *float64                                    `json:"top_k_minimum_score,omitempty"`
	TopKWeightSum       *float64                                    `json:"top_k_weight_sum,omitempty"`
	NormalizationRanges AdvancedSchedulerScoreDiagnosticRanges      `json:"normalization_ranges"`
	Candidates          []AdvancedSchedulerScoreDiagnosticCandidate `json:"candidates"`
}

// AdvancedSchedulerScoreDiagnosticRanges contains the pool ranges used to normalize scores.
type AdvancedSchedulerScoreDiagnosticRanges struct {
	PriorityMin     int      `json:"priority_min"`
	PriorityMax     int      `json:"priority_max"`
	MaxWaitingCount int      `json:"max_waiting_count"`
	TTFTMinMs       *float64 `json:"ttft_min_ms,omitempty"`
	TTFTMaxMs       *float64 `json:"ttft_max_ms,omitempty"`
	ResetMinSeconds *float64 `json:"reset_min_seconds,omitempty"`
	ResetMaxSeconds *float64 `json:"reset_max_seconds,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticCandidate summarizes candidate ranking and selection probability.
type AdvancedSchedulerScoreDiagnosticCandidate struct {
	ID                   int64    `json:"id"`
	Name                 string   `json:"name"`
	Platform             string   `json:"platform"`
	Priority             int      `json:"priority"`
	FinalScore           float64  `json:"final_score"`
	Rank                 int      `json:"rank"`
	InTopK               bool     `json:"in_top_k"`
	SelectionWeight      *float64 `json:"selection_weight,omitempty"`
	SelectionProbability *float64 `json:"selection_probability,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticScore explains the target provider's final score and weighted selection.
type AdvancedSchedulerScoreDiagnosticScore struct {
	BaseScore            float64  `json:"base_score"`
	StickyBonus          float64  `json:"sticky_bonus"`
	FinalScore           float64  `json:"final_score"`
	Rank                 int      `json:"rank"`
	InTopK               bool     `json:"in_top_k"`
	SelectionWeight      *float64 `json:"selection_weight,omitempty"`
	SelectionProbability *float64 `json:"selection_probability,omitempty"`
	SelectionMode        string   `json:"selection_mode"`
	Formula              string   `json:"formula"`
}

// AdvancedSchedulerScoreDiagnosticMetric describes a factor's normalization, weight, and contribution.
type AdvancedSchedulerScoreDiagnosticMetric struct {
	Key                  string     `json:"key"`
	RawValue             string     `json:"raw_value"`
	Normalization        string     `json:"normalization"`
	NormalizedValue      float64    `json:"normalized_value"`
	Weight               float64    `json:"weight"`
	WeightedContribution float64    `json:"weighted_contribution"`
	Available            bool       `json:"available"`
	Neutral              bool       `json:"neutral"`
	Source               string     `json:"source"`
	ObservedAt           *time.Time `json:"observed_at,omitempty"`
}

// AdvancedSchedulerScoreDiagnosticSetting contains an effective value and the source that supplied it.
type AdvancedSchedulerScoreDiagnosticSetting struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// AdvancedSchedulerScoreDiagnosticPolicySignal describes policy and eligibility checks separately from score factors.
type AdvancedSchedulerScoreDiagnosticPolicySignal struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}
