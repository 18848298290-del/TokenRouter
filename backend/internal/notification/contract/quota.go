package contract

// Quota alerts carry the chosen provider dimension and its configured limit
// and threshold into notification rendering. The business caller supplies the
// observed usage, and the renderer displays remaining quota and either a
// currency or percentage threshold.

// QuotaDimension records the configured dimension and usage observations supplied by quota callers.
type QuotaDimension struct {
	Name string // quota dimension name
	// Enabled carries billing's dimension notification switch. AlertDelivery
	// formats already selected alerts and does not consult this switch before sending.
	Enabled bool // dimension notification switch
	// Threshold and ThresholdType select the alert threshold's display format.
	// AlertDelivery prints percentage for the literal percentage type and currency
	// for other types; the caller determines when this threshold was crossed.
	Threshold     float64 // configured alert threshold
	ThresholdType string  // threshold display kind
	// CurrentUsed and Limit carry observations from the business caller in the
	// same quota units. AlertDelivery receives its rendered used amount separately
	// and subtracts that argument from Limit when displaying remaining quota.
	CurrentUsed, Limit float64 // observed usage and configured quota limit
}
