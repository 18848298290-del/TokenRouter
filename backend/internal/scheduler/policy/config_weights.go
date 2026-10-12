package policy

import "math"

// ConfigScoreWeights contains process-level weights converted to ScoreWeights for scoring.
type ConfigScoreWeights struct {
	Priority  float64 `mapstructure:"priority"`
	Load      float64 `mapstructure:"load"`
	Queue     float64 `mapstructure:"queue"`
	ErrorRate float64 `mapstructure:"error_rate"`
	TTFT      float64 `mapstructure:"ttft"`
	// Reset rewards providers whose session windows reset sooner.
	// Positive values favor shorter remaining times; zero disables this factor by default.
	Reset float64 `mapstructure:"reset"`
	// QuotaHeadroom rewards greater remaining Codex 7d quota.
	// Zero disables this factor by default.
	QuotaHeadroom float64 `mapstructure:"quota_headroom"`
	// PreviousResponse and SessionSticky apply when advanced weighted stickiness is enabled.
	PreviousResponse float64 `mapstructure:"previous_response"`
	SessionSticky    float64 `mapstructure:"session_sticky"`
}

func (w ConfigScoreWeights) BaseWeightSum() float64 {
	return w.Priority + w.Load + w.Queue + w.ErrorRate + w.TTFT + w.Reset + w.QuotaHeadroom
}

func (w ConfigScoreWeights) TotalWeightSum() float64 {
	return w.BaseWeightSum() + w.PreviousResponse + w.SessionSticky
}

func (w ConfigScoreWeights) IsValid() bool {
	for _, weight := range []float64{
		w.Priority, w.Load, w.Queue, w.ErrorRate, w.TTFT, w.Reset,
		w.QuotaHeadroom, w.PreviousResponse, w.SessionSticky,
	} {
		if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return false
		}
	}
	baseSum := w.BaseWeightSum()
	return baseSum > 0 && !math.IsNaN(baseSum) && !math.IsInf(baseSum, 0) &&
		!math.IsNaN(w.TotalWeightSum()) && !math.IsInf(w.TotalWeightSum(), 0)
}
