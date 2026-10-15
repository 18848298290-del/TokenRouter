package dto

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// subscription_dto.go defines subscription plans, granted entitlements, and
// administrator assignment response shapes.
// Subscription responses keep the issued quota and validity separate from the
// plan's editable sale template. Hydrated plans show group coverage and
// labels, while administrator wrappers include assignment metadata and batch
// outcomes; user responses expose their granted state and optional revocation
// timestamp.

// SubscriptionPlan carries sale terms, group applicability and issued-quota
// templates in JSON responses.
type SubscriptionPlan struct {
	ID          int64   `json:"id"`          // local subscription plan identifier
	Name        string  `json:"name"`        // selected plan display name
	Description string  `json:"description"` // selected plan description text
	Price       float64 `json:"price"`       // configured plan sale amount
	// OriginalPrice retains the service plan's optional comparison amount
	// pointer. Response consumers keep that shared amount unchanged while reading
	// the plan.
	OriginalPrice *float64 `json:"original_price,omitempty"` // optional comparison display amount
	Currency      string   `json:"currency,omitempty"`       // configured sale currency code
	// ValidityDays and ValidityUnit express the grant duration template together.
	// Grant computation converts weeks to seven days and months to thirty days
	// before issuing the subscription.
	ValidityDays int    `json:"validity_days"` // configured validity quantity
	ValidityUnit string `json:"validity_unit"` // configured validity quantity unit
	// GroupIDs and GroupRateMultipliers define coverage and selected group rates.
	// Empty IDs represent unrestricted coverage, while configured IDs narrow the
	// groups that can spend this subscription.
	GroupIDs             []int64           `json:"group_ids"`              // applicable subscription group identifiers
	GroupRateMultipliers map[int64]float64 `json:"group_rate_multipliers"` // plan group rate overrides
	// GroupsRestricted and ApplicableGroups are display metadata enriched from
	// plan coverage. The mapper sets the flag for a retained restriction flag or
	// nonempty GroupIDs and returns allocated group-label arrays.
	GroupsRestricted bool                    `json:"groups_restricted"` // derived group restriction flag
	ApplicableGroups []SubscriptionPlanGroup `json:"applicable_groups"` // hydrated applicable group labels
	// DailyLimitUSD, WeeklyLimitUSD and MonthlyLimitUSD are plan templates copied
	// at issuance. Nil or nonpositive values represent unlimited windows; later
	// plan changes do not replace granted limits. The mapper retains these
	// optional amount pointers for read-only response consumption.
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`   // USD
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`  // USD
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"` // USD
	Features        string   `json:"features"`          // selected plan feature text
	ProductName     string   `json:"product_name"`      // selected payment product name
	// ForSale determines whether checkout offers the plan for new purchases.
	// Existing subscriptions can still reference this plan after it is taken
	// off sale.
	ForSale   bool      `json:"for_sale"`   // new purchase availability flag
	SortOrder int       `json:"sort_order"` // plan display sort order
	CreatedAt time.Time `json:"created_at"` // plan record creation time
	UpdatedAt time.Time `json:"updated_at"` // plan record update time
}

// SubscriptionPlanGroup identifies a covered group and its display name for
// plan responses.
type SubscriptionPlanGroup struct {
	ID   int64  `json:"id"`   // covered group record identifier
	Name string `json:"name"` // covered group display name
}

// UserSubscription carries granted validity, quota observations and hydrated
// user/plan response data.
type UserSubscription struct {
	ID     int64 `json:"id"`      // local granted subscription identifier
	UserID int64 `json:"user_id"` // grant recipient user identifier
	PlanID int64 `json:"plan_id"` // grant source plan identifier

	// StartsAt and ExpiresAt bound the granted interval with inclusive start and
	// exclusive expiry. Status is supplied by the service view used for this
	// response.
	StartsAt  time.Time `json:"starts_at"`  // inclusive grant start timestamp
	ExpiresAt time.Time `json:"expires_at"` // exclusive grant expiry timestamp
	Status    string    `json:"status"`     // provided subscription status value

	// DailyWindowStart, WeeklyWindowStart and MonthlyWindowStart identify
	// independent quota observations. Their corresponding limits and usage values
	// describe the saved or service-normalized state represented by this
	// response. Their optional pointers remain shared with the service record.
	DailyWindowStart   *time.Time `json:"daily_window_start"`   // stored daily window start
	WeeklyWindowStart  *time.Time `json:"weekly_window_start"`  // stored weekly window start
	MonthlyWindowStart *time.Time `json:"monthly_window_start"` // stored monthly window start

	// DailyLimitUSD, WeeklyLimitUSD and MonthlyLimitUSD are the issued ceilings.
	// Nil or nonpositive values represent unlimited windows, and usage values
	// remain separate for every configured period.
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`   // USD
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`  // USD
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"` // USD

	// DailyUsageUSD, WeeklyUsageUSD and MonthlyUsageUSD contain this view's
	// independent window totals. A charge covered by the subscription contributes
	// to every positive window ceiling.
	DailyUsageUSD   float64 `json:"daily_usage_usd"`   // USD
	WeeklyUsageUSD  float64 `json:"weekly_usage_usd"`  // USD
	MonthlyUsageUSD float64 `json:"monthly_usage_usd"` // USD

	CreatedAt time.Time `json:"created_at"` // grant record creation time
	UpdatedAt time.Time `json:"updated_at"` // grant record update time
	// RevokedAt is mapped from the service record's soft-deletion timestamp. A
	// nil value means that deletion marker is absent in this response view.
	RevokedAt *time.Time `json:"revoked_at,omitempty"` // mapped subscription deletion time

	User *UserSummaryResponse `json:"user,omitempty"` // hydrated grant recipient details
	Plan *SubscriptionPlan    `json:"plan,omitempty"` // hydrated source plan details
}

// AdminUserSubscription adds assignment identity, timestamp and internal notes
// for administrator responses.
type AdminUserSubscription struct {
	UserSubscription // common user subscription response

	// AssignedBy retains the service record's optional administrator ID pointer.
	// A missing pointer represents a system or unrecorded assigner in this
	// response.
	AssignedBy *int64    `json:"assigned_by"` // optional assigning administrator identifier
	AssignedAt time.Time `json:"assigned_at"` // subscription assignment creation time
	Notes      string    `json:"notes"`       // internal subscription assignment notes

	AssignedByUser *UserSummaryResponse `json:"assigned_by_user,omitempty"` // hydrated assigning administrator details
}

// BulkAssignResult carries grant outcomes and per-user status text in batch
// responses.
type BulkAssignResult struct {
	// SuccessCount, CreatedCount, ReusedCount and FailedCount are supplied by the
	// grant service. Subscriptions carries successful records, Errors carries
	// failures, and Statuses uses decimal user-ID keys.
	SuccessCount  int                     `json:"success_count"` // reported successful grant count
	CreatedCount  int                     `json:"created_count"` // reported created grant count
	ReusedCount   int                     `json:"reused_count"`  // reported reused grant count
	FailedCount   int                     `json:"failed_count"`  // reported failed grant count
	Subscriptions []AdminUserSubscription `json:"subscriptions"` // successful administrator grant responses
	// Errors retains the service result's error slice during mapping. Response
	// consumers keep that shared slice unchanged while reading per-user failure
	// messages.
	Errors []string `json:"errors"` // reported user failure messages
	// Statuses maps decimal user-ID text to the service outcome string. The
	// mapper rebuilds these keys so JSON object names retain the user
	// identifiers.
	Statuses map[string]string `json:"statuses,omitempty"` // decimal user-ID outcome map
}

// UserSummaryResponse carries billing user details and preferences in hydrated
// entitlement responses.
type UserSummaryResponse struct {
	ID            int64   `json:"id"`             // billing user record identifier
	Email         string  `json:"email"`          // primary user email address
	Username      string  `json:"username"`       // user display name text
	Role          string  `json:"role"`           // stored user role name
	Balance       float64 `json:"balance"`        // current user balance amount
	FrozenBalance float64 `json:"frozen_balance"` // frozen user balance amount
	Concurrency   int     `json:"concurrency"`    // user concurrent request limit
	Status        string  `json:"status"`         // stored user status name
	// AllowedGroups and DisabledPublicGroups retain the service user's group
	// slices during mapping. Response consumers keep those shared slices
	// unchanged while reading the user's permitted and denied groups.
	AllowedGroups        []int64    `json:"allowed_groups"`           // explicitly permitted group identifiers
	DisabledPublicGroups []int64    `json:"disabled_public_groups"`   // administrator-denied public group identifiers
	LastActiveAt         *time.Time `json:"last_active_at,omitempty"` // latest observed activity time
	CreatedAt            time.Time  `json:"created_at"`               // user record creation time
	UpdatedAt            time.Time  `json:"updated_at"`               // user record update time
	DeletedAt            *time.Time `json:"deleted_at,omitempty"`     // user soft-deletion timestamp

	BalanceNotifyEnabled       bool   `json:"balance_notify_enabled"`        // user balance alert switch
	BalanceNotifyThresholdType string `json:"balance_notify_threshold_type"` // fixed amount or percentage
	// BalanceNotifyThreshold overrides the global alert amount when present. Its
	// kind and cumulative recharge total determine amount conversion; nil retains
	// the configured global threshold.
	BalanceNotifyThreshold   *float64                     `json:"balance_notify_threshold"`    // optional user alert threshold
	BalanceNotifyExtraEmails []billing.NotifyEmailSummary `json:"balance_notify_extra_emails"` // saved additional alert recipients
	TotalRecharged           float64                      `json:"total_recharged"`             // cumulative credited balance amount

	// RPMLimit caps total user requests after scheduler group admission. Positive
	// values apply independently of group ceilings, while zero disables this
	// user-wide check.
	RPMLimit int `json:"rpm_limit"` // requests per minute
	// APIKeyLimit is the user API key creation ceiling. Zero leaves creation
	// unlimited for the service consuming this user record.
	APIKeyLimit int `json:"api_key_limit"` // API key creation limit
}
