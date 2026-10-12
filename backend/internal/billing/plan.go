package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

type SubscriptionPlan struct {
	Localization         PlanLocalization
	Resolution           locale.Resolution
	ID                   int64
	Name                 string
	Description          string
	Price                float64
	OriginalPrice        *float64
	Currency             string
	ValidityDays         int
	ValidityUnit         string
	GroupIDs             []int64
	GroupRateMultipliers map[int64]float64
	GroupsRestricted     bool
	ApplicableGroups     []SubscriptionPlanGroup
	DailyLimitUSD        *float64
	WeeklyLimitUSD       *float64
	MonthlyLimitUSD      *float64
	Features             string
	ProductName          string
	ForSale              bool
	SortOrder            int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SubscriptionPlanGroup identifies a group covered by a plan and supplies its display name.
type SubscriptionPlanGroup struct {
	ID   int64
	Name string
}
