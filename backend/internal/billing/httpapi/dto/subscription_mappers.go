package dto

import (
	"maps"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// subscription_mappers.go converts billing subscriptions and hydrated plan or
// user records into JSON response values.
// Subscription JSON contains both grant snapshots and hydrated plan/user data.
// Mapping copies group collections and contact records selectively while
// retaining several optional pointers, so response consumers need to keep
// shared service values unchanged. DeletedAt becomes the user-visible
// RevokedAt timestamp, while assignment details are added by the administrator
// wrapper.
//
// Response shape also carries information. Contact cloning retains nil versus
// an allocated empty slice, while plan rate maps and applicable-group arrays
// use allocated empty results. Bulk status maps convert numeric user IDs to
// decimal object keys so per-user outcomes remain identifiable after JSON
// encoding.

// SubscriptionPlanFromServiceShallow maps a plan for response consumption,
// returning nil for nil input. Group IDs, rate maps and applicable-group
// values are copied, while comparison-price and quota pointers remain shared
// with the service plan. Callers keep those pointers unchanged while using
// this response.
//
// GroupsRestricted is true when either the supplied flag is retained or
// GroupIDs is nonempty. ApplicableGroups and the rate map are allocated even
// when empty, so their JSON response representations remain arrays and objects
// rather than null.
func SubscriptionPlanFromServiceShallow(plan *billing.SubscriptionPlan) *SubscriptionPlan {
	if plan == nil {
		return nil
	}
	out := &SubscriptionPlan{
		ID:                   plan.ID,
		Name:                 plan.Name,
		Description:          plan.Description,
		Price:                plan.Price,
		OriginalPrice:        plan.OriginalPrice,
		Currency:             plan.Currency,
		ValidityDays:         plan.ValidityDays,
		ValidityUnit:         plan.ValidityUnit,
		GroupIDs:             append([]int64(nil), plan.GroupIDs...),
		GroupRateMultipliers: CloneInt64Float64Map(plan.GroupRateMultipliers),
		GroupsRestricted:     plan.GroupsRestricted || len(plan.GroupIDs) > 0,
		DailyLimitUSD:        plan.DailyLimitUSD,
		WeeklyLimitUSD:       plan.WeeklyLimitUSD,
		MonthlyLimitUSD:      plan.MonthlyLimitUSD,
		Features:             plan.Features,
		ProductName:          plan.ProductName,
		ForSale:              plan.ForSale,
		SortOrder:            plan.SortOrder,
		CreatedAt:            plan.CreatedAt,
		UpdatedAt:            plan.UpdatedAt,
	}
	out.ApplicableGroups = make([]SubscriptionPlanGroup, 0, len(plan.ApplicableGroups))
	for _, group := range plan.ApplicableGroups {
		out.ApplicableGroups = append(out.ApplicableGroups, SubscriptionPlanGroup{ID: group.ID, Name: group.Name})
	}
	// A nil group-label slice would encode as null; this branch selects an
	// allocated empty array for the plan response.
	if out.ApplicableGroups == nil {
		out.ApplicableGroups = []SubscriptionPlanGroup{}
	}
	return out
}

// UserSubscriptionFromService returns the user subscription DTO or nil for nil
// input. It delegates the common mapping, which retains optional dates and
// quota pointers and maps hydrated user/plan data for read-only response
// consumption.
func UserSubscriptionFromService(sub *billing.UserSubscription) *UserSubscription {
	if sub == nil {
		return nil
	}
	out := UserSubscriptionFromServiceBase(sub)
	return &out
}

// UserSubscriptionFromServiceAdmin returns the administrator DTO or nil for
// nil input. It adds assignment data and full notes to the common mapping;
// optional assignment and quota/date pointers remain shared with the service
// record.
func UserSubscriptionFromServiceAdmin(sub *billing.UserSubscription) *AdminUserSubscription {
	if sub == nil {
		return nil
	}
	return &AdminUserSubscription{
		UserSubscription: UserSubscriptionFromServiceBase(sub),
		AssignedBy:       sub.AssignedBy,
		AssignedAt:       sub.AssignedAt,
		Notes:            sub.Notes,
		AssignedByUser:   UserSummaryFromBilling(sub.AssignedByUser),
	}
}

// UserSubscriptionFromServiceBase maps a non-nil subscription into the common
// response. DeletedAt supplies RevokedAt; optional window, quota and deletion
// pointers remain shared, while hydrated plan and user mappings apply their
// own selective-copy rules. Status is copied as supplied by the service view.
func UserSubscriptionFromServiceBase(sub *billing.UserSubscription) UserSubscription {
	return UserSubscription{
		ID:                 sub.ID,
		UserID:             sub.UserID,
		PlanID:             sub.PlanID,
		StartsAt:           sub.StartsAt,
		ExpiresAt:          sub.ExpiresAt,
		Status:             sub.Status,
		DailyWindowStart:   sub.DailyWindowStart,
		WeeklyWindowStart:  sub.WeeklyWindowStart,
		MonthlyWindowStart: sub.MonthlyWindowStart,
		DailyLimitUSD:      sub.DailyLimitUSD,
		WeeklyLimitUSD:     sub.WeeklyLimitUSD,
		MonthlyLimitUSD:    sub.MonthlyLimitUSD,
		DailyUsageUSD:      sub.DailyUsageUSD,
		WeeklyUsageUSD:     sub.WeeklyUsageUSD,
		MonthlyUsageUSD:    sub.MonthlyUsageUSD,
		CreatedAt:          sub.CreatedAt,
		UpdatedAt:          sub.UpdatedAt,
		RevokedAt:          sub.DeletedAt,
		User:               UserSummaryFromBilling(sub.User),
		Plan:               SubscriptionPlanFromServiceShallow(sub.Plan),
	}
}

// BulkAssignResultFromService maps a batch grant result, returning nil for nil
// input. It allocates administrator response records and a new status map
// whose keys are decimal user IDs. Counter values and errors come from the
// service result, with the error slice retained.
//
// The new string-key map preserves each source user identity in the JSON
// object, including when its outcome string is empty. Successful records
// retain the shared pointers described by their mapper; callers consume the
// result as a read-only response.
func BulkAssignResultFromService(r *billing.BulkAssignResult) *BulkAssignResult {
	if r == nil {
		return nil
	}
	subs := make([]AdminUserSubscription, 0, len(r.Subscriptions))
	for i := range r.Subscriptions {
		subs = append(subs, *UserSubscriptionFromServiceAdmin(&r.Subscriptions[i]))
	}
	// JSON status object keys need user identity as text; rebuild each numeric
	// key in decimal while retaining its service outcome value.
	statuses := make(map[string]string, len(r.Statuses))
	for userID, status := range r.Statuses {
		statuses[strconv.FormatInt(userID, 10)] = status
	}
	return &BulkAssignResult{
		SuccessCount:  r.SuccessCount,
		CreatedCount:  r.CreatedCount,
		ReusedCount:   r.ReusedCount,
		FailedCount:   r.FailedCount,
		Subscriptions: subs,
		Errors:        r.Errors,
		Statuses:      statuses,
	}
}

// CloneInt64Float64Map returns independent storage for the supplied group-rate
// values. Nil and empty maps both produce an allocated empty map, keeping
// response JSON as an object; scalar keys and amounts are copied.
func CloneInt64Float64Map(in map[int64]float64) map[int64]float64 {
	// Plan responses expect a group-rate object even when no overrides exist, so
	// both nil and empty inputs use allocated map storage.
	if len(in) == 0 {
		return map[int64]float64{}
	}
	out := make(map[int64]float64, len(in))
	maps.Copy(out, in)
	return out
}

// UserSummaryFromBilling maps a billing user or returns nil for nil input.
// Group slices, optional dates and threshold pointers remain shared; extra
// email records are copied while retaining nil versus allocated-empty slice
// shape. Callers keep shared values unchanged during response use.
func UserSummaryFromBilling(u *billing.UserSummary) *UserSummaryResponse {
	if u == nil {
		return nil
	}
	return &UserSummaryResponse{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		AllowedGroups:              u.AllowedGroups,
		DisabledPublicGroups:       u.DisabledPublicGroups,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		BalanceNotifyExtraEmails:   CloneNotifyEmailEntries(u.BalanceNotifyExtraEmails),
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RPMLimit,
		APIKeyLimit:                u.APIKeyLimit,
		DeletedAt:                  u.DeletedAt,
	}
}

// CloneNotifyEmailEntries returns copied contact records while retaining the
// input slice shape. Nil stays nil, and an allocated empty input yields an
// allocated empty result, preserving the corresponding JSON null versus empty
// array.
func CloneNotifyEmailEntries(v []billing.NotifyEmailSummary) []billing.NotifyEmailSummary {
	// A nil contact list encodes differently from an allocated empty list; retain
	// this distinction when copying the user's response data.
	if v == nil {
		return nil
	}
	out := make([]billing.NotifyEmailSummary, len(v))
	copy(out, v)
	return out
}
