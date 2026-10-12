package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const MaxValidityDays = 36500

var (
	ErrSubscriptionAlreadyExists = apperror.Conflict("SUBSCRIPTION_ALREADY_EXISTS", "subscription already exists")
	ErrSubscriptionNilInput      = apperror.BadRequest("SUBSCRIPTION_NIL_INPUT", "subscription input cannot be nil")
	ErrSubscriptionNotFound      = apperror.NotFound("SUBSCRIPTION_NOT_FOUND", "subscription not found")
	MaxExpiresAt                 = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

	ErrSubscriptionExpired         = apperror.Forbidden("SUBSCRIPTION_EXPIRED", "subscription has expired")
	ErrSubscriptionSuspended       = apperror.Forbidden("SUBSCRIPTION_SUSPENDED", "subscription is suspended")
	ErrSubscriptionNotRevoked      = apperror.Conflict("SUBSCRIPTION_NOT_REVOKED", "subscription is not revoked")
	ErrSubscriptionRestoreConflict = apperror.Conflict("SUBSCRIPTION_RESTORE_CONFLICT", "subscription already exists for this user and plan")
	ErrSubscriptionNotActive       = apperror.Conflict("SUBSCRIPTION_NOT_ACTIVE", "subscription is not active")
	ErrSubscriptionQuotaAvailable  = apperror.Conflict("SUBSCRIPTION_QUOTA_NOT_EXHAUSTED", "subscription still has available quota")
	ErrInvalidInput                = apperror.BadRequest("INVALID_INPUT", "at least one of resetDaily, resetWeekly, or resetMonthly must be true")
	ErrDailyLimitExceeded          = apperror.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily usage limit exceeded")
	ErrWeeklyLimitExceeded         = apperror.TooManyRequests("WEEKLY_LIMIT_EXCEEDED", "weekly usage limit exceeded")
	ErrMonthlyLimitExceeded        = apperror.TooManyRequests("MONTHLY_LIMIT_EXCEEDED", "monthly usage limit exceeded")
	ErrAdjustWouldExpire           = apperror.BadRequest("ADJUST_WOULD_EXPIRE", "adjustment would result in invalid subscription window")

	ErrSubscriptionInvalid = apperror.Forbidden("SUBSCRIPTION_INVALID", "subscription is invalid or expired")
)

// SubscriptionGroupReader reads group names for subscription displays.
type SubscriptionGroupReader interface {
	GetByIDLite(context.Context, int64) (*SubscriptionPlanGroup, error)
}

// SubscriptionTransactions runs entitlement mutations in a shared persistence transaction.
// Callbacks use the transaction carried by context; the adapter defines lock order.
type SubscriptionTransactions interface {
	HasPersistence(context.Context) bool
	Within(context.Context, func(context.Context) error) error
	GetPlan(context.Context, int64) (*SubscriptionPlan, error)
	LockGrantUser(context.Context, int64) error
	LockSubscription(context.Context, int64) error
	LockSelfRevoke(context.Context, int64, int64) error
	RebindKeys(context.Context, int64, int64) (int, error)
}

// SubscriptionService manages subscription eligibility, renewal chains, quota windows, and displays.
type SubscriptionService struct {
	groupRepo    SubscriptionGroupReader
	userSubRepo  UserSubscriptionRepository
	transactions SubscriptionTransactions
	clock        DateRuntime
}

// SelfRevokeSubscriptionResult identifies the replacement subscription and rebound keys after revocation.
type SelfRevokeSubscriptionResult struct {
	RevokedSubscriptionID     int64
	ReplacementSubscriptionID *int64
	ReboundAPIKeyCount        int
}

type AssignSubscriptionInput struct {
	UserID              int64
	PlanID              int64
	ValidityDays        int
	DailyLimitUSD       *float64
	WeeklyLimitUSD      *float64
	MonthlyLimitUSD     *float64
	UseProvidedTemplate bool
	SourceOrderID       *int64
	AssignedBy          int64
	Notes               string
}

type GrantPlanTemplate struct {
	ValidityDays    int
	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64
}

type BulkAssignSubscriptionInput struct {
	UserIDs         []int64
	PlanID          int64
	ValidityDays    int
	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64
	AssignedBy      int64
	Notes           string
}

type BulkAssignResult struct {
	SuccessCount  int
	CreatedCount  int
	ReusedCount   int
	FailedCount   int
	Subscriptions []UserSubscription
	Errors        []string
	Statuses      map[int64]string
}

type UserSubscriptionGroupFilter interface {
	FilterByGroup(ctx context.Context, subs []UserSubscription, groupID int64) ([]UserSubscription, error)
}

type SubscriptionProgress struct {
	ID            int64                `json:"id"`
	PlanID        int64                `json:"plan_id"`
	PlanName      string               `json:"plan_name"`
	StartsAt      time.Time            `json:"starts_at"`
	ExpiresAt     time.Time            `json:"expires_at"`
	Status        string               `json:"status"`
	ExpiresInDays int                  `json:"expires_in_days"`
	Daily         *UsageWindowProgress `json:"daily,omitempty"`
	Weekly        *UsageWindowProgress `json:"weekly,omitempty"`
	Monthly       *UsageWindowProgress `json:"monthly,omitempty"`
}

type UsageWindowProgress struct {
	LimitUSD        float64   `json:"limit_usd"`
	UsedUSD         float64   `json:"used_usd"`
	RemainingUSD    float64   `json:"remaining_usd"`
	Percentage      float64   `json:"percentage"`
	WindowStart     time.Time `json:"window_start"`
	ResetsAt        time.Time `json:"resets_at"`
	ResetsInSeconds int64     `json:"resets_in_seconds"`
}

// NewSubscriptionService constructs the subscription service with its repositories and clock.
func NewSubscriptionService(groups SubscriptionGroupReader, repo UserSubscriptionRepository, transactions SubscriptionTransactions, clocks ...DateRuntime) *SubscriptionService {
	clock := DateRuntime{}
	if len(clocks) > 0 {
		clock = clocks[0]
	}
	return &SubscriptionService{groupRepo: groups, userSubRepo: repo, transactions: transactions, clock: clock}
}

// EnrichSubscriptionPlanGroups populates group names and marks plans with no group IDs as unrestricted.
func (s *SubscriptionService) EnrichSubscriptionPlanGroups(ctx context.Context, subscriptions []UserSubscription) {
	if s == nil {
		return
	}
	for i := range subscriptions {
		plan := subscriptions[i].Plan
		if plan == nil {
			continue
		}
		plan.GroupsRestricted = len(plan.GroupIDs) > 0
		if len(plan.GroupIDs) == 0 || len(plan.ApplicableGroups) > 0 {
			continue
		}
		plan.ApplicableGroups = make([]SubscriptionPlanGroup, 0, len(plan.GroupIDs))
		for _, groupID := range plan.GroupIDs {
			group := &SubscriptionPlanGroup{ID: groupID}
			if s.groupRepo != nil {
				if resolved, err := s.groupRepo.GetByIDLite(ctx, groupID); err == nil && resolved != nil {
					group.Name = resolved.Name
				}
			}
			plan.ApplicableGroups = append(plan.ApplicableGroups, *group)
		}
	}
}

func (s *SubscriptionService) Stop() {}

func (s *SubscriptionService) resolveGrantPlanTemplate(ctx context.Context, input *AssignSubscriptionInput) (*GrantPlanTemplate, error) {
	if input == nil || input.PlanID <= 0 {
		return nil, fmt.Errorf("assign subscription: invalid plan_id")
	}

	template := &GrantPlanTemplate{}
	if input.UseProvidedTemplate {
		template.ValidityDays = NormalizeAssignValidityDays(input.ValidityDays)
		template.DailyLimitUSD = input.DailyLimitUSD
		template.WeeklyLimitUSD = input.WeeklyLimitUSD
		template.MonthlyLimitUSD = input.MonthlyLimitUSD
		if err := ValidatePlanQuotas(template.DailyLimitUSD, template.WeeklyLimitUSD, template.MonthlyLimitUSD); err != nil {
			return nil, err
		}
		return template, nil
	}
	if !s.transactions.HasPersistence(ctx) {
		template.ValidityDays = NormalizeAssignValidityDays(input.ValidityDays)
		template.DailyLimitUSD = input.DailyLimitUSD
		template.WeeklyLimitUSD = input.WeeklyLimitUSD
		template.MonthlyLimitUSD = input.MonthlyLimitUSD
		if err := ValidatePlanQuotas(template.DailyLimitUSD, template.WeeklyLimitUSD, template.MonthlyLimitUSD); err != nil {
			return nil, err
		}
		return template, nil
	}

	plan, err := s.transactions.GetPlan(ctx, input.PlanID)
	if err != nil {
		return nil, fmt.Errorf("assign subscription: get plan %d: %w", input.PlanID, err)
	}

	template.ValidityDays = NormalizeAssignValidityDays(ComputeValidityDays(plan.ValidityDays, plan.ValidityUnit))
	template.DailyLimitUSD = plan.DailyLimitUSD
	template.WeeklyLimitUSD = plan.WeeklyLimitUSD
	template.MonthlyLimitUSD = plan.MonthlyLimitUSD
	if input.ValidityDays > 0 {
		template.ValidityDays = NormalizeAssignValidityDays(input.ValidityDays)
	}
	if input.DailyLimitUSD != nil {
		template.DailyLimitUSD = input.DailyLimitUSD
	}
	if input.WeeklyLimitUSD != nil {
		template.WeeklyLimitUSD = input.WeeklyLimitUSD
	}
	if input.MonthlyLimitUSD != nil {
		template.MonthlyLimitUSD = input.MonthlyLimitUSD
	}
	if err := ValidatePlanQuotas(template.DailyLimitUSD, template.WeeklyLimitUSD, template.MonthlyLimitUSD); err != nil {
		return nil, err
	}
	return template, nil
}

func (s *SubscriptionService) AssignSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	sub, _, err := s.AssignOrExtendSubscription(ctx, input)
	return sub, err
}

func (s *SubscriptionService) AssignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	if input == nil {
		return nil, false, ErrSubscriptionNilInput
	}
	if existing, found, err := s.findSourceOrderSubscription(ctx, input.SourceOrderID); err != nil {
		return nil, false, err
	} else if found {
		return existing, existing.IsPending(), nil
	}

	template, err := s.resolveGrantPlanTemplate(ctx, input)
	if err != nil {
		return nil, false, err
	}

	if !s.transactions.HasPersistence(ctx) {
		return s.assignOrExtendSubscriptionUnlocked(ctx, input, template)
	}
	var result *UserSubscription
	var queued bool
	err = s.transactions.Within(ctx, func(txCtx context.Context) error {
		var e error
		result, queued, e = s.assignOrExtendSubscriptionInTx(txCtx, input, template)
		return e
	})
	return result, queued, err
}

func (s *SubscriptionService) findSourceOrderSubscription(ctx context.Context, sourceOrderID *int64) (*UserSubscription, bool, error) {
	if sourceOrderID == nil || *sourceOrderID <= 0 {
		return nil, false, nil
	}
	subs, err := s.userSubRepo.ListBySourceOrderID(ctx, *sourceOrderID)
	if err != nil {
		return nil, false, err
	}
	if len(subs) == 0 {
		return nil, false, nil
	}
	NormalizeSubscriptionStatus(subs)
	return &subs[0], true, nil
}

func (s *SubscriptionService) assignOrExtendSubscriptionInTx(ctx context.Context, input *AssignSubscriptionInput, template *GrantPlanTemplate) (*UserSubscription, bool, error) {
	if err := s.transactions.LockGrantUser(ctx, input.UserID); err != nil {
		return nil, false, err
	}
	if existing, found, err := s.findSourceOrderSubscription(ctx, input.SourceOrderID); err != nil {
		return nil, false, err
	} else if found {
		return existing, existing.IsPending(), nil
	}
	return s.assignOrExtendSubscriptionUnlocked(ctx, input, template)
}

func (s *SubscriptionService) assignOrExtendSubscriptionUnlocked(ctx context.Context, input *AssignSubscriptionInput, template *GrantPlanTemplate) (*UserSubscription, bool, error) {
	now := s.clock.now()
	latest, err := s.userSubRepo.GetLatestByUserIDAndPlanID(ctx, input.UserID, input.PlanID)
	if err != nil {
		latest = nil
	}

	startsAt := now
	queued := false
	if latest != nil && latest.ExpiresAt.After(now) {
		startsAt = latest.ExpiresAt
		queued = true
	}
	expiresAt := startsAt.AddDate(0, 0, template.ValidityDays)
	if expiresAt.After(MaxExpiresAt) {
		expiresAt = MaxExpiresAt
	}

	status := SubscriptionStatusActive
	if startsAt.After(now) {
		status = SubscriptionStatusPending
	}

	sub := &UserSubscription{
		UserID:          input.UserID,
		PlanID:          input.PlanID,
		StartsAt:        startsAt,
		ExpiresAt:       expiresAt,
		Status:          status,
		DailyLimitUSD:   template.DailyLimitUSD,
		WeeklyLimitUSD:  template.WeeklyLimitUSD,
		MonthlyLimitUSD: template.MonthlyLimitUSD,
		AssignedAt:      now,
		SourceOrderID:   input.SourceOrderID,
		Notes:           input.Notes,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if input.AssignedBy > 0 {
		sub.AssignedBy = &input.AssignedBy
	}

	if err := s.userSubRepo.Create(ctx, sub); err != nil {
		return nil, false, err
	}
	created, err := s.userSubRepo.GetByID(ctx, sub.ID)
	if err != nil {
		return nil, false, err
	}
	return created, queued, nil
}

func (s *SubscriptionService) BulkAssignSubscription(ctx context.Context, input *BulkAssignSubscriptionInput) (*BulkAssignResult, error) {
	result := &BulkAssignResult{
		Subscriptions: make([]UserSubscription, 0),
		Errors:        make([]string, 0),
		Statuses:      make(map[int64]string),
	}

	for _, userID := range input.UserIDs {
		sub, queued, err := s.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID:          userID,
			PlanID:          input.PlanID,
			ValidityDays:    input.ValidityDays,
			DailyLimitUSD:   input.DailyLimitUSD,
			WeeklyLimitUSD:  input.WeeklyLimitUSD,
			MonthlyLimitUSD: input.MonthlyLimitUSD,
			AssignedBy:      input.AssignedBy,
			Notes:           input.Notes,
		})
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("user %d: %v", userID, err))
			result.Statuses[userID] = "failed"
			continue
		}
		result.SuccessCount++
		result.CreatedCount++
		result.Subscriptions = append(result.Subscriptions, *sub)
		if queued {
			result.Statuses[userID] = "queued"
		} else {
			result.Statuses[userID] = "active"
		}
	}

	return result, nil
}

func NormalizeAssignValidityDays(days int) int {
	if days <= 0 {
		days = 30
	}
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	return days
}

func (s *SubscriptionService) revokeSubscriptionLocked(ctx context.Context, subscriptionID int64) error {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return err
	}

	chain, err := s.userSubRepo.ListByUserIDAndPlanID(ctx, sub.UserID, sub.PlanID)
	if err != nil {
		return err
	}

	now := s.clock.now()
	chainDelta := RevokeChainDelta(sub, now)
	return s.withSubscriptionMutationTx(ctx, func(txCtx context.Context) error {
		if err := s.userSubRepo.Delete(txCtx, sub.ID); err != nil {
			return err
		}
		if chainDelta != 0 {
			if err := s.ShiftLaterChain(txCtx, chain, sub, chainDelta); err != nil {
				return err
			}
		}
		return nil
	})
}

// RevokeOwnExhaustedSubscription revokes an active subscription owned by the user when its outermost finite quota is exhausted.
// Revocation, shifting later subscriptions, and rebinding subscription-funded keys share one transaction.
func (s *SubscriptionService) RevokeOwnExhaustedSubscription(ctx context.Context, userID, subscriptionID int64) (*SelfRevokeSubscriptionResult, error) {
	if userID <= 0 || subscriptionID <= 0 {
		return nil, ErrSubscriptionNotFound
	}
	var result *SelfRevokeSubscriptionResult
	err := s.transactions.Within(ctx, func(txCtx context.Context) error {
		var e error
		result, e = s.revokeOwnExhaustedSubscriptionInTx(txCtx, userID, subscriptionID)
		return e
	})
	return result, err
}

func (s *SubscriptionService) revokeOwnExhaustedSubscriptionInTx(ctx context.Context, userID, subscriptionID int64) (*SelfRevokeSubscriptionResult, error) {
	sub, err := s.userSubRepo.GetByIDIncludeDeleted(ctx, subscriptionID)
	if err != nil || sub == nil || sub.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	if sub.DeletedAt != nil {
		return nil, ErrSubscriptionNotActive
	}

	if err := s.transactions.LockSelfRevoke(ctx, userID, subscriptionID); err != nil {
		return nil, err
	}

	now := s.clock.now()
	if sub.EffectiveStatus(now) != SubscriptionStatusActive {
		return nil, ErrSubscriptionNotActive
	}
	if err := s.CheckAndActivateWindow(ctx, sub); err != nil {
		return nil, err
	}
	if err := s.CheckAndResetWindows(ctx, sub); err != nil {
		return nil, err
	}
	sub, err = s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil || sub == nil || sub.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	now = s.clock.now()
	if sub.EffectiveStatus(now) != SubscriptionStatusActive {
		return nil, ErrSubscriptionNotActive
	}
	if !sub.HighestQuotaExhausted() {
		return nil, ErrSubscriptionQuotaAvailable
	}

	chain, err := s.userSubRepo.ListByUserIDAndPlanID(ctx, sub.UserID, sub.PlanID)
	if err != nil {
		return nil, err
	}
	var replacement *UserSubscription
	for i := range chain {
		item := chain[i]
		if item.ID == sub.ID ||
			item.StartsAt.Before(sub.ExpiresAt) ||
			!item.ExpiresAt.After(now) ||
			item.Status != SubscriptionStatusPending ||
			item.EffectiveStatus(now) != SubscriptionStatusPending {
			continue
		}
		if replacement == nil || item.StartsAt.Before(replacement.StartsAt) {
			candidate := item
			replacement = &candidate
		}
	}

	if err := s.userSubRepo.Delete(ctx, sub.ID); err != nil {
		return nil, err
	}
	if delta := RevokeChainDelta(sub, now); delta != 0 {
		if err := s.ShiftLaterChain(ctx, chain, sub, delta); err != nil {
			return nil, err
		}
	}

	result := &SelfRevokeSubscriptionResult{RevokedSubscriptionID: sub.ID}
	if replacement != nil {
		result.ReplacementSubscriptionID = &replacement.ID
		count, err := s.transactions.RebindKeys(ctx, sub.ID, replacement.ID)
		if err != nil {
			return nil, err
		}
		result.ReboundAPIKeyCount = count
	}
	return result, nil
}

// RestoreSubscription restores a revoked subscription after checking its renewal chain.
func (s *SubscriptionService) RestoreSubscription(ctx context.Context, subscriptionID int64) (*UserSubscription, error) {
	if !s.transactions.HasPersistence(ctx) {
		return s.restoreSubscriptionUnlocked(ctx, subscriptionID)
	}
	var result *UserSubscription
	err := s.transactions.Within(ctx, func(txCtx context.Context) error {
		var e error
		result, e = s.restoreSubscriptionInTx(txCtx, subscriptionID)
		return e
	})
	return result, err
}

func (s *SubscriptionService) restoreSubscriptionInTx(ctx context.Context, subscriptionID int64) (*UserSubscription, error) {
	initial, err := s.userSubRepo.GetByIDIncludeDeleted(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if err := s.transactions.LockGrantUser(ctx, initial.UserID); err != nil {
		return nil, err
	}
	return s.restoreSubscriptionUnlocked(ctx, subscriptionID)
}

func (s *SubscriptionService) restoreSubscriptionUnlocked(ctx context.Context, subscriptionID int64) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByIDIncludeDeleted(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if sub.DeletedAt == nil {
		return nil, ErrSubscriptionNotRevoked
	}

	now := s.clock.now()
	restoredStatus := RestoredSubscriptionStatus(sub, now)
	if restoredStatus != SubscriptionStatusExpired {
		chain, err := s.userSubRepo.ListByUserIDAndPlanID(ctx, sub.UserID, sub.PlanID)
		if err != nil {
			return nil, err
		}
		if SubscriptionRestoreWouldOverlap(sub, chain, now) {
			return nil, ErrSubscriptionRestoreConflict
		}
	}

	return s.userSubRepo.Restore(ctx, subscriptionID, restoredStatus)
}

// RestoredSubscriptionStatus computes the effective status after clearing the deletion marker.
func RestoredSubscriptionStatus(sub *UserSubscription, now time.Time) string {
	if sub == nil {
		return SubscriptionStatusExpired
	}
	cp := *sub
	cp.DeletedAt = nil
	return cp.EffectiveStatus(now)
}

// SubscriptionRestoreWouldOverlap reports whether restoration overlaps an undeleted, unexpired entry in the supplied chain.
func SubscriptionRestoreWouldOverlap(restored *UserSubscription, chain []UserSubscription, now time.Time) bool {
	if restored == nil {
		return false
	}
	for i := range chain {
		item := chain[i]
		if item.ID == restored.ID || item.DeletedAt != nil || !item.ExpiresAt.After(now) {
			continue
		}
		if restored.StartsAt.Before(item.ExpiresAt) && item.StartsAt.Before(restored.ExpiresAt) {
			return true
		}
	}
	return false
}

func RevokeChainDelta(sub *UserSubscription, now time.Time) time.Duration {
	if sub == nil {
		return 0
	}
	if now.Before(sub.StartsAt) {
		return sub.StartsAt.Sub(sub.ExpiresAt)
	}
	if sub.ExpiresAt.After(now) {
		return now.Sub(sub.ExpiresAt)
	}
	return 0
}

func (s *SubscriptionService) extendSubscriptionLocked(ctx context.Context, subscriptionID int64, days int) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	if days < -MaxValidityDays {
		days = -MaxValidityDays
	}

	now := s.clock.now()
	oldExpiresAt := sub.ExpiresAt
	var newExpiresAt time.Time
	if !oldExpiresAt.After(now) {
		if days < 0 {
			return nil, ErrAdjustWouldExpire
		}
		newExpiresAt = now.AddDate(0, 0, days)
	} else {
		newExpiresAt = oldExpiresAt.AddDate(0, 0, days)
	}
	if newExpiresAt.After(MaxExpiresAt) {
		newExpiresAt = MaxExpiresAt
	}
	if !newExpiresAt.After(sub.StartsAt) {
		return nil, ErrAdjustWouldExpire
	}

	delta := newExpiresAt.Sub(oldExpiresAt)
	chain, err := s.userSubRepo.ListByUserIDAndPlanID(ctx, sub.UserID, sub.PlanID)
	if err != nil {
		return nil, err
	}

	err = s.withSubscriptionMutationTx(ctx, func(txCtx context.Context) error {
		if err := s.userSubRepo.ExtendExpiry(txCtx, subscriptionID, newExpiresAt); err != nil {
			return err
		}
		if delta != 0 {
			if err := s.ShiftLaterChain(txCtx, chain, sub, delta); err != nil {
				return err
			}
		}

		var status string
		if !newExpiresAt.After(now) {
			status = SubscriptionStatusExpired
		} else if now.Before(sub.StartsAt) {
			status = SubscriptionStatusPending
		} else {
			status = SubscriptionStatusActive
		}
		return s.userSubRepo.UpdateStatus(txCtx, subscriptionID, status)
	})
	if err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

func (s *SubscriptionService) withSubscriptionMutationTx(ctx context.Context, mutate func(context.Context) error) error {
	return s.transactions.Within(ctx, mutate)
}

func (s *SubscriptionService) setSubscriptionValidityDaysLocked(ctx context.Context, subscriptionID int64, validityDays int) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	if validityDays <= 0 {
		return nil, ErrAdjustWouldExpire
	}
	if validityDays > MaxValidityDays {
		validityDays = MaxValidityDays
	}

	now := s.clock.now()
	oldExpiresAt := sub.ExpiresAt
	newExpiresAt := TargetSubscriptionExpiresAt(sub, now, validityDays)
	if newExpiresAt.After(MaxExpiresAt) {
		newExpiresAt = MaxExpiresAt
	}
	if !newExpiresAt.After(sub.StartsAt) {
		return nil, ErrAdjustWouldExpire
	}

	delta := newExpiresAt.Sub(oldExpiresAt)
	chain, err := s.userSubRepo.ListByUserIDAndPlanID(ctx, sub.UserID, sub.PlanID)
	if err != nil {
		return nil, err
	}

	err = s.transactions.Within(ctx, func(txCtx context.Context) error {
		if err := s.userSubRepo.ExtendExpiry(txCtx, subscriptionID, newExpiresAt); err != nil {
			return err
		}
		if delta != 0 {
			if err := s.ShiftLaterChain(txCtx, chain, sub, delta); err != nil {
				return err
			}
		}

		var status string
		if !newExpiresAt.After(now) {
			status = SubscriptionStatusExpired
		} else if now.Before(sub.StartsAt) {
			status = SubscriptionStatusPending
		} else {
			status = SubscriptionStatusActive
		}
		return s.userSubRepo.UpdateStatus(txCtx, subscriptionID, status)
	})
	if err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

func TargetSubscriptionExpiresAt(sub *UserSubscription, now time.Time, validityDays int) time.Time {
	return SubscriptionValidityAnchor(sub, now).AddDate(0, 0, validityDays)
}

func SubscriptionValidityAnchor(sub *UserSubscription, now time.Time) time.Time {
	if sub != nil && now.Before(sub.StartsAt) {
		return sub.StartsAt
	}
	return now
}

func (s *SubscriptionService) ShiftLaterChain(ctx context.Context, chain []UserSubscription, anchor *UserSubscription, delta time.Duration) error {
	if delta == 0 || anchor == nil {
		return nil
	}
	now := s.clock.now()
	for i := range chain {
		item := chain[i]
		if item.ID == anchor.ID {
			continue
		}
		if item.StartsAt.Before(anchor.ExpiresAt) {
			continue
		}
		item.StartsAt = item.StartsAt.Add(delta)
		item.ExpiresAt = item.ExpiresAt.Add(delta)
		if item.ExpiresAt.After(MaxExpiresAt) {
			item.ExpiresAt = MaxExpiresAt
		}
		switch {
		case !item.ExpiresAt.After(now):
			item.Status = SubscriptionStatusExpired
		case now.Before(item.StartsAt):
			item.Status = SubscriptionStatusPending
		default:
			item.Status = SubscriptionStatusActive
		}
		if err := s.userSubRepo.Update(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionService) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.clock.now()
	sub.Status = sub.EffectiveStatus(now)
	return sub, nil
}

// GetSubscriptionForAPIKey reads the subscription belonging to the paying user.
// Ownership mismatches return ErrSubscriptionNotFound to prevent probing other users' subscription IDs.
func (s *SubscriptionService) GetSubscriptionForAPIKey(ctx context.Context, userID, subscriptionID int64) (*UserSubscription, error) {
	if s == nil || s.userSubRepo == nil || userID <= 0 || subscriptionID <= 0 {
		return nil, ErrSubscriptionNotFound
	}
	subscription, err := s.GetByID(ctx, subscriptionID)
	if err != nil || subscription == nil || subscription.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	return subscription, nil
}

func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, userID, planID int64) (*UserSubscription, error) {
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range subs {
		if subs[i].PlanID == planID {
			return &subs[i], nil
		}
	}
	return nil, ErrSubscriptionNotFound
}

func (s *SubscriptionService) GetUsableSubscription(ctx context.Context, userID int64, groupID ...int64) (*UserSubscription, bool, error) {
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if len(groupID) > 0 && groupID[0] > 0 {
		subs, err = s.filterSubscriptionsByGroup(ctx, subs, groupID[0])
		if err != nil {
			return nil, false, err
		}
	}
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].ExpiresAt.Equal(subs[j].ExpiresAt) {
			return subs[i].StartsAt.Before(subs[j].StartsAt)
		}
		return subs[i].ExpiresAt.Before(subs[j].ExpiresAt)
	})
	for i := range subs {
		sub := &subs[i]
		needsMaintenance, validateErr := s.ValidateAndCheckLimits(sub)
		if validateErr == nil {
			return sub, needsMaintenance, nil
		}
		if errors.Is(validateErr, ErrDailyLimitExceeded) ||
			errors.Is(validateErr, ErrWeeklyLimitExceeded) ||
			errors.Is(validateErr, ErrMonthlyLimitExceeded) ||
			errors.Is(validateErr, ErrSubscriptionExpired) ||
			errors.Is(validateErr, ErrSubscriptionSuspended) ||
			errors.Is(validateErr, ErrSubscriptionInvalid) {
			continue
		}
		return nil, false, validateErr
	}
	return nil, false, ErrSubscriptionNotFound
}

func (s *SubscriptionService) filterSubscriptionsByGroup(ctx context.Context, subs []UserSubscription, groupID int64) ([]UserSubscription, error) {
	if groupID <= 0 || len(subs) == 0 {
		return subs, nil
	}
	if s == nil || s.userSubRepo == nil {
		return nil, fmt.Errorf("subscription plan group filter unavailable")
	}
	filter, ok := s.userSubRepo.(UserSubscriptionGroupFilter)
	if !ok {
		return nil, fmt.Errorf("subscription plan group filter unavailable")
	}
	return filter.FilterByGroup(ctx, subs, groupID)
}

func (s *SubscriptionService) ListUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs, s.clock.calendar())
	NormalizeSubscriptionStatus(subs)
	return subs, nil
}

func (s *SubscriptionService) ListActiveUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs, s.clock.calendar())
	NormalizeSubscriptionStatus(subs)
	return subs, nil
}

func (s *SubscriptionService) ListSubscriptionsBySourceOrderID(ctx context.Context, sourceOrderID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListBySourceOrderID(ctx, sourceOrderID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs, s.clock.calendar())
	NormalizeSubscriptionStatus(subs)
	return subs, nil
}

func (s *SubscriptionService) ListPlanSubscriptions(ctx context.Context, planID int64, page, pageSize int) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.ListByPlanID(ctx, planID, params)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs, s.clock.calendar())
	NormalizeSubscriptionStatus(subs)
	return subs, pag, nil
}

func (s *SubscriptionService) List(ctx context.Context, page, pageSize int, userID, planID *int64, status, _platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.List(ctx, params, userID, planID, status, "", sortBy, sortOrder)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs, s.clock.calendar())
	NormalizeSubscriptionStatus(subs)
	return subs, pag, nil
}

func normalizeExpiredWindows(subs []UserSubscription, calendar timezone.Calendar) {
	for i := range subs {
		sub := &subs[i]
		if sub.NeedsDailyReset(calendar) {
			sub.DailyWindowStart = nil
			sub.DailyUsageUSD = 0
		}
		if sub.NeedsWeeklyReset() {
			sub.WeeklyWindowStart = nil
			sub.WeeklyUsageUSD = 0
		}
		if sub.NeedsMonthlyReset() {
			sub.MonthlyWindowStart = nil
			sub.MonthlyUsageUSD = 0
		}
	}
}

func NormalizeSubscriptionStatus(subs []UserSubscription) {
	now := time.Now()
	for i := range subs {
		subs[i].Status = subs[i].EffectiveStatus(now)
	}
}

func (s *SubscriptionService) CheckAndActivateWindow(ctx context.Context, sub *UserSubscription) error {
	now := s.clock.now()
	activation := sub.WindowActivationAt(now)
	if !activation.Any() {
		return nil
	}
	windowStart := startOfDay(now)
	return s.userSubRepo.ActivateWindows(ctx, sub.ID, windowStart, activation)
}

func (s *SubscriptionService) adminResetQuotaLocked(ctx context.Context, subscriptionID int64, resetDaily, resetWeekly, resetMonthly bool) (*UserSubscription, error) {
	if !resetDaily && !resetWeekly && !resetMonthly {
		return nil, ErrInvalidInput
	}
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	windowStart := startOfDay(s.clock.now())
	if err := s.userSubRepo.ResetUsageWindows(ctx, sub.ID, resetDaily, resetWeekly, resetMonthly, windowStart); err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

func (s *SubscriptionService) CheckAndResetWindows(ctx context.Context, sub *UserSubscription) error {
	return s.CheckAndResetWindowsAt(ctx, sub, s.clock.now())
}

// CheckAndResetWindowsAt evaluates all quota windows at now so a midnight crossing cannot split the reset decisions.
func (s *SubscriptionService) CheckAndResetWindowsAt(ctx context.Context, sub *UserSubscription, now time.Time) error {
	windowStart := startOfDay(now)
	if dailyWindowStart, ok := sub.AutomaticDailyWindowStartWithCalendar(now, s.clock.calendar()); ok {
		expectedWindowStart := sub.DailyWindowStart
		if err := s.userSubRepo.ResetDailyUsage(ctx, sub.ID, expectedWindowStart, dailyWindowStart); err != nil {
			return err
		}
		sub.DailyWindowStart = &dailyWindowStart
		sub.DailyUsageUSD = 0
	}
	if sub.NeedsWeeklyResetAt(now) {
		expectedWindowStart := sub.WeeklyWindowStart
		if err := s.userSubRepo.ResetWeeklyUsage(ctx, sub.ID, expectedWindowStart, windowStart); err != nil {
			return err
		}
		sub.WeeklyWindowStart = &windowStart
		sub.WeeklyUsageUSD = 0
	}
	if sub.NeedsMonthlyResetAt(now) {
		expectedWindowStart := sub.MonthlyWindowStart
		if err := s.userSubRepo.ResetMonthlyUsage(ctx, sub.ID, expectedWindowStart, windowStart); err != nil {
			return err
		}
		sub.MonthlyWindowStart = &windowStart
		sub.MonthlyUsageUSD = 0
	}
	return nil
}

// EnsureWindowMaintenance activates or resets quota windows and rereads the stored subscription.
// A concurrent request may win the conditional reset; rereading prevents admission using locally cleared usage.
func (s *SubscriptionService) EnsureWindowMaintenance(ctx context.Context, sub *UserSubscription) (*UserSubscription, error) {
	if sub == nil {
		return nil, ErrSubscriptionNilInput
	}
	if err := s.CheckAndActivateWindow(ctx, sub); err != nil {
		return nil, err
	}
	if err := s.CheckAndResetWindows(ctx, sub); err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, sub.ID)
}

func (s *SubscriptionService) CheckUsageLimits(_ context.Context, sub *UserSubscription, additionalCost float64) error {
	return CheckSubscriptionUsageLimits(sub, additionalCost)
}

func CheckSubscriptionUsageLimits(sub *UserSubscription, additionalCost float64) error {
	if SubscriptionWindowLimitExceeded(sub.DailyLimitUSD, sub.DailyUsageUSD, additionalCost) {
		return ErrDailyLimitExceeded
	}
	if SubscriptionWindowLimitExceeded(sub.WeeklyLimitUSD, sub.WeeklyUsageUSD, additionalCost) {
		return ErrWeeklyLimitExceeded
	}
	if SubscriptionWindowLimitExceeded(sub.MonthlyLimitUSD, sub.MonthlyUsageUSD, additionalCost) {
		return ErrMonthlyLimitExceeded
	}
	return nil
}

func SubscriptionWindowLimitExceeded(limit *float64, used float64, additionalCost float64) bool {
	if limit == nil || *limit <= 0 {
		return false
	}
	// A zero additional cost is an admission check before the final cost is known, so some quota must remain.
	if additionalCost == 0 {
		return used >= *limit
	}
	return used+additionalCost > *limit
}

// ValidateAndCheckLimits performs an in-memory precheck and clears stale usage in sub.
// When needsMaintenance is true, callers must run EnsureWindowMaintenance and recheck its result before admission.
func (s *SubscriptionService) ValidateAndCheckLimits(sub *UserSubscription) (needsMaintenance bool, err error) {
	switch sub.EffectiveStatus(s.clock.now()) {
	case SubscriptionStatusExpired:
		return false, ErrSubscriptionExpired
	case SubscriptionStatusSuspended:
		return false, ErrSubscriptionSuspended
	case SubscriptionStatusPending:
		return false, ErrSubscriptionInvalid
	case SubscriptionStatusRevoked:
		return false, ErrSubscriptionNotFound
	}
	if sub.NeedsDailyReset(s.clock.calendar()) {
		sub.DailyUsageUSD = 0
		needsMaintenance = true
	}
	if sub.NeedsWeeklyReset() {
		sub.WeeklyUsageUSD = 0
		needsMaintenance = true
	}
	if sub.NeedsMonthlyReset() {
		sub.MonthlyUsageUSD = 0
		needsMaintenance = true
	}
	if sub.NeedsWindowActivationAt(s.clock.now()) {
		needsMaintenance = true
	}
	return needsMaintenance, s.CheckUsageLimits(context.Background(), sub, 0)
}

func (s *SubscriptionService) DoWindowMaintenance(sub *UserSubscription) {
	if sub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sub.NeedsWindowActivationAt(s.clock.now()) {
		_ = s.CheckAndActivateWindow(ctx, sub)
	}
	_ = s.CheckAndResetWindows(ctx, sub)
}

func (s *SubscriptionService) GetSubscriptionProgress(ctx context.Context, subscriptionID int64) (*SubscriptionProgress, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	if locale.UserPresentation(ctx) {
		localized := *sub
		localized.Plan = LocalizePlan(sub.Plan, locale.FromContext(ctx))
		sub = &localized
	}
	return s.CalculateProgress(sub), nil
}

func (s *SubscriptionService) CalculateProgress(sub *UserSubscription) *SubscriptionProgress {
	now := s.clock.now()
	progress := &SubscriptionProgress{
		ID:            sub.ID,
		PlanID:        sub.PlanID,
		StartsAt:      sub.StartsAt,
		ExpiresAt:     sub.ExpiresAt,
		Status:        sub.EffectiveStatus(now),
		ExpiresInDays: sub.DaysRemaining(),
	}
	if sub.Plan != nil {
		progress.PlanName = sub.Plan.Name
	}
	if limit, ok := NormalizedWindowProgress(sub.DailyLimitUSD, sub.DailyUsageUSD, sub.DailyResetTime(s.clock.calendar()), sub.DailyWindowStart, SubscriptionDailyWindow); ok {
		progress.Daily = limit
	}
	if limit, ok := NormalizedWindowProgress(sub.WeeklyLimitUSD, sub.WeeklyUsageUSD, sub.WeeklyResetTime(), sub.WeeklyWindowStart, SubscriptionWeeklyWindow); ok {
		progress.Weekly = limit
	}
	if limit, ok := NormalizedWindowProgress(sub.MonthlyLimitUSD, sub.MonthlyUsageUSD, sub.MonthlyResetTime(), sub.MonthlyWindowStart, SubscriptionMonthlyWindow); ok {
		progress.Monthly = limit
	}
	return progress
}

func NormalizedWindowProgress(limit *float64, used float64, resetAt, windowStart *time.Time, duration time.Duration) (*UsageWindowProgress, bool) {
	if limit == nil || *limit <= 0 || windowStart == nil {
		return nil, false
	}
	resetsAt := windowStart.Add(duration)
	if resetAt != nil {
		resetsAt = *resetAt
	}
	remaining := *limit - used
	if remaining < 0 {
		remaining = 0
	}
	percentage := 0.0
	if *limit > 0 {
		percentage = (used / *limit) * 100
		if percentage > 100 {
			percentage = 100
		}
	}
	resetsIn := max(int64(time.Until(resetsAt).Seconds()), 0)
	return &UsageWindowProgress{
		LimitUSD:        *limit,
		UsedUSD:         used,
		RemainingUSD:    remaining,
		Percentage:      percentage,
		WindowStart:     *windowStart,
		ResetsAt:        resetsAt,
		ResetsInSeconds: resetsIn,
	}, true
}

// RevokeSubscription revokes the subscription and shifts its renewal chain under the persistence lock.
func (s *SubscriptionService) RevokeSubscription(ctx context.Context, subscriptionID int64) error {
	_, err := withLockedSubscription(s, ctx, subscriptionID, func(txCtx context.Context) (struct{}, error) {
		return struct{}{}, s.revokeSubscriptionLocked(txCtx, subscriptionID)
	})
	return err
}

// ExtendSubscription extends the subscription and shifts later entries under the persistence lock.
func (s *SubscriptionService) ExtendSubscription(ctx context.Context, subscriptionID int64, days int) (*UserSubscription, error) {
	return withLockedSubscription(s, ctx, subscriptionID, func(txCtx context.Context) (*UserSubscription, error) {
		return s.extendSubscriptionLocked(txCtx, subscriptionID, days)
	})
}

// SetSubscriptionValidityDays changes the expiry and shifts later entries under the persistence lock.
func (s *SubscriptionService) SetSubscriptionValidityDays(ctx context.Context, subscriptionID int64, validityDays int) (*UserSubscription, error) {
	return withLockedSubscription(s, ctx, subscriptionID, func(txCtx context.Context) (*UserSubscription, error) {
		return s.setSubscriptionValidityDaysLocked(txCtx, subscriptionID, validityDays)
	})
}

// AdminResetQuota resets the selected usage windows under the persistence lock.
func (s *SubscriptionService) AdminResetQuota(ctx context.Context, subscriptionID int64, resetDaily, resetWeekly, resetMonthly bool) (*UserSubscription, error) {
	if !resetDaily && !resetWeekly && !resetMonthly {
		return nil, ErrInvalidInput
	}
	return withLockedSubscription(s, ctx, subscriptionID, func(txCtx context.Context) (*UserSubscription, error) {
		return s.adminResetQuotaLocked(txCtx, subscriptionID, resetDaily, resetWeekly, resetMonthly)
	})
}

// withLockedSubscription rereads and mutates subscriptions in one locked transaction when persistence is available.
func withLockedSubscription[T any](s *SubscriptionService, ctx context.Context, id int64, fn func(context.Context) (T, error)) (T, error) {
	if !s.transactions.HasPersistence(ctx) {
		return fn(ctx)
	}
	var result T
	err := s.transactions.Within(ctx, func(txCtx context.Context) error {
		if err := s.transactions.LockSubscription(txCtx, id); err != nil {
			return err
		}
		var err error
		result, err = fn(txCtx)
		return err
	})
	return result, err
}
