package accessview

// A group's displayed capacity combines provider limits with current runtime
// counts. These values describe the providers observed by CapacityService, so
// console consumers use them as aggregate availability information while
// request admission checks its own current limits.

// GroupCapacitySummary holds provider capacity totals and their observed
// runtime usage.
type GroupCapacitySummary struct {
	GroupID         int64 `json:"group_id"`         // group receiving these totals
	ConcurrencyUsed int   `json:"concurrency_used"` // occupied provider request slots
	ConcurrencyMax  int   `json:"concurrency_max"`  // summed provider concurrency limits
	// SessionsUsed is read across included providers when SessionsMax is positive.
	// SessionsMax sums positive provider session limits; unavailable runtime
	// counts contribute zero to the displayed usage.
	SessionsUsed int `json:"sessions_used"` // observed active provider sessions
	SessionsMax  int `json:"sessions_max"`  // summed positive session limits
	// RPMUsed is read across included providers when RPMMax is positive. RPMMax
	// sums positive provider minute ceilings, while missing runtime observations
	// contribute zero to the summed usage.
	RPMUsed int `json:"rpm_used"` // observed provider minute counts
	RPMMax  int `json:"rpm_max"`  // summed positive RPM limits
}
