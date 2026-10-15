package ops

import "time"

// realtime_models.go defines realtime concurrency and availability response
// shapes for providers, groups, platforms, and users.
// Realtime provider observations separate configured display capacity from
// scheduler activity, and one provider can contribute to several group totals.
// Availability badges combine saved status with current hold times; an error
// status clears rate-limit and overload badges before the administrator
// response is built.

// PlatformConcurrencyInfo carries accumulated provider capacity, active slots,
// and waiting requests for one platform.
type PlatformConcurrencyInfo struct {
	Platform string `json:"platform"` // observed provider platform label
	// CurrentInUse and WaitingInQueue sum loads for selected providers with a
	// nonempty Platform. MaxCapacity sums their configured concurrency;
	// LoadPercentage expresses total CurrentInUse divided by positive MaxCapacity
	// as a percentage, and otherwise remains zero.
	CurrentInUse   int64   `json:"current_in_use"`   // observed active concurrency slots
	MaxCapacity    int64   `json:"max_capacity"`     // configured displayed concurrency capacity
	LoadPercentage float64 `json:"load_percentage"`  // percent
	WaitingInQueue int64   `json:"waiting_in_queue"` // observed pending request count
}

// GroupConcurrencyInfo carries matching provider capacity and activity for one
// observed group.
type GroupConcurrencyInfo struct {
	// GroupID and GroupName identify the displayed membership. A provider can
	// contribute the same load and capacity to several groups, so these group
	// totals overlap.
	GroupID   int64  `json:"group_id"`   // observed group membership identity
	GroupName string `json:"group_name"` // current group display name
	// CurrentInUse and WaitingInQueue sum scheduler loads for each matching
	// membership. MaxCapacity sums configured concurrency for those providers, so
	// group totals can overlap; LoadPercentage uses the summed positive capacity
	// and otherwise remains zero.
	CurrentInUse   int64   `json:"current_in_use"`   // observed active concurrency slots
	MaxCapacity    int64   `json:"max_capacity"`     // configured displayed concurrency capacity
	LoadPercentage float64 `json:"load_percentage"`  // percent
	WaitingInQueue int64   `json:"waiting_in_queue"` // observed pending request count
}

// ProviderConcurrencyInfo carries one provider load observation and the group
// displayed beside it.
type ProviderConcurrencyInfo struct {
	ProviderID   int64  `json:"provider_id"`   // observed execution provider identity
	ProviderName string `json:"provider_name"` // current provider display name
	Platform     string `json:"platform"`      // observed provider platform label
	// GroupID and GroupName identify the selected matching group when a filter is
	// supplied, or the first provider membership otherwise. Providers with no
	// displayed membership retain zero and empty name values.
	GroupID   int64  `json:"group_id"`   // displayed provider group identity
	GroupName string `json:"group_name"` // displayed provider group name
	// CurrentInUse and WaitingInQueue use the scheduler observation for this provider and
	// default to zero when it is missing. MaxCapacity uses its configured
	// concurrency, with LoadPercentage computed for positive capacity and zero
	// otherwise.
	CurrentInUse   int64   `json:"current_in_use"`   // observed active concurrency slots
	MaxCapacity    int64   `json:"max_capacity"`     // configured displayed concurrency capacity
	LoadPercentage float64 `json:"load_percentage"`  // percent
	WaitingInQueue int64   `json:"waiting_in_queue"` // observed pending request count
}

// UserConcurrencyInfo carries one active-user load observation for the realtime
// administrator view.
type UserConcurrencyInfo struct {
	UserID    int64  `json:"user_id"`    // observed active user identity
	UserEmail string `json:"user_email"` // current user email address
	Username  string `json:"username"`   // current user display name
	// CurrentInUse and WaitingInQueue use the observed user load, and
	// GetUserConcurrencyStats includes rows with running or waiting activity.
	// MaxCapacity uses configured user concurrency. LoadPercentage expresses
	// CurrentInUse divided by positive MaxCapacity as a percentage, and remains
	// zero for nonpositive capacity.
	CurrentInUse   int64   `json:"current_in_use"`   // observed active concurrency slots
	MaxCapacity    int64   `json:"max_capacity"`     // configured displayed concurrency capacity
	LoadPercentage float64 `json:"load_percentage"`  // percent
	WaitingInQueue int64   `json:"waiting_in_queue"` // observed pending request count
}

// PlatformAvailability carries provider availability counts within an observed
// platform.
type PlatformAvailability struct {
	Platform string `json:"platform"` // observed provider platform label
	// TotalProviders counts selected entries with this nonempty platform.
	// AvailableCount counts entries passing the Ops status-and-hold check,
	// RateLimitCount counts active rate-limit badges, and ErrorCount counts error
	// status, which clears rate-limit badges.
	TotalProviders int64 `json:"total_providers"`  // observed provider entry count
	AvailableCount int64 `json:"available_count"`  // entries passing availability calculation
	RateLimitCount int64 `json:"rate_limit_count"` // entries with active rate-limit badge
	ErrorCount     int64 `json:"error_count"`      // entries carrying error status
}

// GroupAvailability carries provider availability counts within one observed
// group.
type GroupAvailability struct {
	GroupID   int64  `json:"group_id"`   // observed group membership identity
	GroupName string `json:"group_name"` // current group display name
	// TotalProviders counts entries with this matching membership, so providers
	// can contribute to several groups. AvailableCount applies the Ops
	// status-and-hold check; RateLimitCount counts active rate-limit badges, and
	// ErrorCount counts error status, which clears those badges.
	TotalProviders int64 `json:"total_providers"`  // observed provider entry count
	AvailableCount int64 `json:"available_count"`  // entries passing availability calculation
	RateLimitCount int64 `json:"rate_limit_count"` // entries with active rate-limit badge
	ErrorCount     int64 `json:"error_count"`      // entries carrying error status
}

// ProviderAvailability carries current status and hold diagnostics for one
// observed provider.
type ProviderAvailability struct {
	ProviderID   int64  `json:"provider_id"`   // observed execution provider identity
	ProviderName string `json:"provider_name"` // current provider display name
	Platform     string `json:"platform"`      // observed provider platform label
	// GroupID and GroupName display a selected matching group or the first
	// membership when unfiltered. An unavailable displayed membership leaves zero
	// ID and empty name; group aggregate totals still use every applicable
	// membership.
	GroupID   int64  `json:"group_id"`   // displayed provider group identity
	GroupName string `json:"group_name"` // displayed provider group name

	Status string `json:"status"` // saved provider status label

	// IsAvailable requires active status, scheduling enabled, and no active
	// rate-limit, overload, or temporary hold. HasError identifies error status
	// and clears IsRateLimited and IsOverloaded so error badges take display
	// precedence.
	IsAvailable   bool `json:"is_available"`    // Ops availability calculation result
	IsRateLimited bool `json:"is_rate_limited"` // active rate-limit diagnostic badge
	IsOverloaded  bool `json:"is_overloaded"`   // active overload diagnostic badge
	HasError      bool `json:"has_error"`       // provider error status badge

	// RateLimitResetAt and RateLimitRemainingSec are exposed for an active
	// rate-limit badge; OverloadUntil and OverloadRemainingSec follow the same
	// rule for overload. Remaining seconds are truncated during formatting and
	// omitted when that integer is nonpositive.
	RateLimitResetAt      *time.Time `json:"rate_limit_reset_at"`      // active rate-limit end time
	RateLimitRemainingSec *int64     `json:"rate_limit_remaining_sec"` // seconds
	OverloadUntil         *time.Time `json:"overload_until"`           // active overload end time
	OverloadRemainingSec  *int64     `json:"overload_remaining_sec"`   // seconds
	ErrorMessage          string     `json:"error_message"`            // saved provider diagnostic text
	// TempUnschedulableUntil contains a future scheduling hold observed during
	// collection. The pointer is included while that hold is active, and the hold
	// makes IsAvailable false.
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until,omitempty"`
}
