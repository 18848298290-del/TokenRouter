package dto

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

type SubscriptionPlan struct {
	ID                   int64                   `json:"id"`
	Name                 string                  `json:"name"`
	Description          string                  `json:"description"`
	Price                float64                 `json:"price"`
	OriginalPrice        *float64                `json:"original_price,omitempty"`
	Currency             string                  `json:"currency,omitempty"`
	ValidityDays         int                     `json:"validity_days"`
	ValidityUnit         string                  `json:"validity_unit"`
	GroupIDs             []int64                 `json:"group_ids"`
	GroupRateMultipliers map[int64]float64       `json:"group_rate_multipliers"`
	GroupsRestricted     bool                    `json:"groups_restricted"`
	ApplicableGroups     []SubscriptionPlanGroup `json:"applicable_groups"`
	DailyLimitUSD        *float64                `json:"daily_limit_usd"`
	WeeklyLimitUSD       *float64                `json:"weekly_limit_usd"`
	MonthlyLimitUSD      *float64                `json:"monthly_limit_usd"`
	Features             string                  `json:"features"`
	ProductName          string                  `json:"product_name"`
	ForSale              bool                    `json:"for_sale"`
	SortOrder            int                     `json:"sort_order"`
	CreatedAt            time.Time               `json:"created_at"`
	UpdatedAt            time.Time               `json:"updated_at"`
}

type SubscriptionPlanGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserSubscription struct {
	ID     int64 `json:"id"`
	UserID int64 `json:"user_id"`
	PlanID int64 `json:"plan_id"`

	StartsAt  time.Time `json:"starts_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Status    string    `json:"status"`

	DailyWindowStart   *time.Time `json:"daily_window_start"`
	WeeklyWindowStart  *time.Time `json:"weekly_window_start"`
	MonthlyWindowStart *time.Time `json:"monthly_window_start"`

	DailyLimitUSD   *float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`

	DailyUsageUSD   float64 `json:"daily_usage_usd"`
	WeeklyUsageUSD  float64 `json:"weekly_usage_usd"`
	MonthlyUsageUSD float64 `json:"monthly_usage_usd"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	User *UserSummaryResponse `json:"user,omitempty"`
	Plan *SubscriptionPlan    `json:"plan,omitempty"`
}

// AdminUserSubscription includes assignment metadata and notes for administrators.
// User endpoints use UserSubscription to exclude assigned_by, assigned_at, notes, and assigned_by_user.
type AdminUserSubscription struct {
	UserSubscription

	AssignedBy *int64    `json:"assigned_by"`
	AssignedAt time.Time `json:"assigned_at"`
	Notes      string    `json:"notes"`

	AssignedByUser *UserSummaryResponse `json:"assigned_by_user,omitempty"`
}

type BulkAssignResult struct {
	SuccessCount  int                     `json:"success_count"`
	CreatedCount  int                     `json:"created_count"`
	ReusedCount   int                     `json:"reused_count"`
	FailedCount   int                     `json:"failed_count"`
	Subscriptions []AdminUserSubscription `json:"subscriptions"`
	Errors        []string                `json:"errors"`
	Statuses      map[string]string       `json:"statuses,omitempty"`
}

type UserSummaryResponse struct {
	ID            int64   `json:"id"`
	Email         string  `json:"email"`
	Username      string  `json:"username"`
	Role          string  `json:"role"`
	Balance       float64 `json:"balance"`
	FrozenBalance float64 `json:"frozen_balance"`
	Concurrency   int     `json:"concurrency"`
	Status        string  `json:"status"`
	AllowedGroups []int64 `json:"allowed_groups"`
	// DisabledPublicGroups lists public group IDs denied to the user by an administrator.
	DisabledPublicGroups []int64    `json:"disabled_public_groups"`
	LastActiveAt         *time.Time `json:"last_active_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	DeletedAt            *time.Time `json:"deleted_at,omitempty"`

	// Low-balance alert settings.
	BalanceNotifyEnabled       bool                         `json:"balance_notify_enabled"`
	BalanceNotifyThresholdType string                       `json:"balance_notify_threshold_type"`
	BalanceNotifyThreshold     *float64                     `json:"balance_notify_threshold"`
	BalanceNotifyExtraEmails   []billing.NotifyEmailSummary `json:"balance_notify_extra_emails"`
	TotalRecharged             float64                      `json:"total_recharged"`

	// RPMLimit is the user request-per-minute limit when the group has no rpm_limit; zero is unlimited.
	RPMLimit int `json:"rpm_limit"`
	// APIKeyLimit caps the number of API Keys the user may create; zero is unlimited.
	APIKeyLimit int `json:"api_key_limit"`
}
