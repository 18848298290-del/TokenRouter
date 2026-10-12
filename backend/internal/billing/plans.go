package billing

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

type PlanRepository interface {
	ListPlans(context.Context) ([]*SubscriptionPlan, error)
	ListPlansForSale(context.Context) ([]*SubscriptionPlan, error)
	CreatePlan(context.Context, CreatePlanRequest) (*SubscriptionPlan, error)
	UpdatePlan(context.Context, int64, UpdatePlanRequest) (*SubscriptionPlan, error)
	DeletePlan(context.Context, int64) error
	GetPlan(context.Context, int64) (*SubscriptionPlan, error)
}

// PlanOrders counts orders that prevent plan deletion; payment owns the qualifying order states.
type PlanOrders interface {
	CountInProgressByPlan(context.Context, int64) (int, error)
}
type Plans struct {
	store  PlanRepository
	orders PlanOrders
}

func NewPlans(store PlanRepository, orders PlanOrders) *Plans {
	return &Plans{store: store, orders: orders}
}

func (s *Plans) ListPlans(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.store.ListPlans(ctx)
}

func (s *Plans) ListPlansForSale(ctx context.Context) ([]*SubscriptionPlan, error) {
	items, err := s.store.ListPlansForSale(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i] = LocalizePlan(items[i], locale.FromContext(ctx))
	}
	return items, nil
}

func (s *Plans) GetPlan(ctx context.Context, id int64) (*SubscriptionPlan, error) {
	return s.store.GetPlan(ctx, id)
}

func (s *Plans) CreatePlan(ctx context.Context, req CreatePlanRequest) (*SubscriptionPlan, error) {
	if req.Localization != nil {
		copy := req.Localization.Source
		req.Name, req.Description, req.Features, req.ProductName = copy.Name, copy.Description, copy.Features, copy.ProductName
	}
	groupIDs := NormalizePlanGroupIDs(req.GroupID, req.GroupIDs)
	groupRates, err := NormalizePlanGroupRateMultipliers(groupIDs, req.GroupRateMultipliers)
	if err != nil {
		return nil, err
	}
	if err := ValidatePlanRequired(req.Name, req.Price, req.ValidityDays, req.ValidityUnit, req.OriginalPrice); err != nil {
		return nil, err
	}
	currency, err := NormalizePlanCurrency(req.Currency)
	if err != nil {
		return nil, err
	}
	if err := ValidatePlanQuotas(req.DailyLimitUSD, req.WeeklyLimitUSD, req.MonthlyLimitUSD); err != nil {
		return nil, err
	}

	req.GroupID = 0
	req.GroupIDs = groupIDs
	req.GroupRateMultipliers = groupRates
	req.Currency = currency
	return s.store.CreatePlan(ctx, req)
}

func (s *Plans) UpdatePlan(ctx context.Context, id int64, req UpdatePlanRequest) (*SubscriptionPlan, error) {
	// Text changes carry a content revision so source text and stale translations update together.
	if req.Localization == nil && (req.Name != nil || req.Description != nil || req.Features != nil || req.ProductName != nil) {
		return nil, apperror.BadRequest("LOCALIZATION_REQUIRED", "Use localization to update plan text.")
	}
	if req.Localization != nil {
		copy := req.Localization.Source
		req.Name, req.Description, req.Features, req.ProductName = &copy.Name, &copy.Description, &copy.Features, &copy.ProductName
	}
	if err := ValidatePlanPatch(req); err != nil {
		return nil, err
	}
	return s.store.UpdatePlan(ctx, id, req)
}

func (s *Plans) DeletePlan(ctx context.Context, id int64) error {
	count, err := s.orders.CountInProgressByPlan(ctx, id)
	if err != nil {
		return fmt.Errorf("check pending orders: %w", err)
	}
	if count > 0 {
		return apperror.Conflict("PENDING_ORDERS", fmt.Sprintf("this plan has %d in-progress orders and cannot be deleted — wait for orders to complete first", count))
	}
	return s.store.DeletePlan(ctx, id)
}
