package usage

// ProviderStats summarizes provider usage with separate cost measures.
//
// Cost sums COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1).
// StandardCost sums total_cost before multipliers.
// UserCost sums actual_cost, the amount charged to users and API keys.
type ProviderStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`
}
