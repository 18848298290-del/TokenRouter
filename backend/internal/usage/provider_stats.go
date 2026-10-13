package usage

// Provider reports keep execution cost, unmultiplied standard cost, and user
// charges as distinct measures. The same request and token counts accompany
// those amounts, so filtering by provider does not change the meaning of the
// user-charge column.

// ProviderStats summarizes one provider's matching usage records with three
// separate monetary measures.
type ProviderStats struct {
	Requests int64 `json:"requests"` // matching usage record count
	Tokens   int64 `json:"tokens"`   // input output and cache tokens
	// Cost, StandardCost, and UserCost use different stored amounts. Cost sums
	// COALESCE(provider_stats_cost, total_cost) times
	// COALESCE(provider_rate_multiplier, 1);
	// StandardCost sums total_cost, and UserCost sums actual_cost.
	Cost         float64 `json:"cost"`          // multiplied provider cost amount
	StandardCost float64 `json:"standard_cost"` // unmultiplied recorded total cost
	UserCost     float64 `json:"user_cost"`     // recorded actual user charges
}
