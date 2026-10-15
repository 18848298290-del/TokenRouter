package billing

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// plans.go prepares plan mutations and exposes plan queries through repository
// adapters.
// Plan text updates carry versioned localization alongside plain
// fields used by existing display and checkout readers. When source
// content is supplied, the service derives those plain fields from
// it before validating the request, while the repository owns
// content revision checks and transactional writes.
//
// Creation also merges the legacy group choice with the configured
// list and restricts group rates to the resulting selection. Ordinary
// update pointers and nullable patches keep their distinct preserve,
// clear, and replacement meanings as they reach storage. Deletion
// checks the payment-owned in-progress order count before requesting
// a delete; those separate calls provide a guard but establish no
// transaction or reservation for the plan here.

// PlanRepository reads and persists configured plan terms and localized content.
type PlanRepository interface {
	// ListPlans returns stored plan terms for administration and service
	// readers. The PostgreSQL adapter sorts by configured display order;
	// the facade returns these values as supplied, including localized
	// content available for later resolution.
	ListPlans(context.Context) ([]*SubscriptionPlan, error)
	// ListPlansForSale returns currently offered plan values from storage.
	// Return a slice whose elements the facade can replace with shallow
	// localized display views; the PostgreSQL adapter supplies a new
	// ordered result slice.
	ListPlansForSale(context.Context) ([]*SubscriptionPlan, error)
	// CreatePlan persists the normalized creation request and returns the
	// recorded plan. The PostgreSQL adapter prepares localization and
	// writes the plan and group mappings in a transaction; validation
	// errors from that storage preparation are returned to the caller.
	CreatePlan(context.Context, CreatePlanRequest) (*SubscriptionPlan, error)
	// UpdatePlan applies requested pointer and presence-aware replacements
	// to a plan. The PostgreSQL adapter handles group-rate normalization
	// and localized-content revision checks, preserving omitted fields
	// and returning the updated recorded value or its persistence error.
	UpdatePlan(context.Context, int64, UpdatePlanRequest) (*SubscriptionPlan, error)
	// DeletePlan removes a plan from storage after the facade's separate
	// order-count check. The PostgreSQL adapter performs the entity delete;
	// this interface call does not receive or retain a payment-order
	// reservation from that earlier check.
	DeletePlan(context.Context, int64) error
	// GetPlan returns the requested stored plan for service callers.
	// The PostgreSQL adapter maps read errors to PLAN_NOT_FOUND; callers
	// that need language-specific display apply LocalizePlan to the
	// returned plan value.
	GetPlan(context.Context, int64) (*SubscriptionPlan, error)
}

// PlanOrders supplies the payment-owned count used to guard configured plan deletion.
type PlanOrders interface {
	// CountInProgressByPlan counts qualifying orders associated with
	// the plan ID. Payment defines the included states; Plans.DeletePlan
	// blocks a positive count and propagates count failures before
	// requesting the independent repository delete.
	CountInProgressByPlan(context.Context, int64) (int, error)
}

// Plans prepares sale-term changes and delegates reads and persistence to its adapters.
type Plans struct {
	store PlanRepository // plan persistence adapter
	// orders supplies payment counts when DeletePlan is called. The
	// constructor retains the supplied adapter as provided, so callers
	// bind a usable count source before requesting deletion.
	orders PlanOrders
}

// NewPlans constructs plan operations using the supplied storage and
// order-count adapters. Bind usable storage before repository calls
// and a usable count adapter before DeletePlan; the constructor
// retains both dependencies as supplied.
func NewPlans(store PlanRepository, orders PlanOrders) *Plans {
	return &Plans{store: store, orders: orders}
}

// ListPlans returns the configured repository's plan list unchanged.
// Storage owns ordering, record construction, and errors; this method
// does not resolve display language, so administrator readers retain
// the recorded plan content.
func (s *Plans) ListPlans(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.store.ListPlans(ctx)
}

// ListPlansForSale returns offered plans localized for the context
// language. It replaces pointers in the repository's returned slice
// with LocalizePlan views; their nested references remain shared
// with the stored values, and query errors return before localization.
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

// GetPlan returns the requested repository plan and its error as
// supplied by storage. Callers establish access to the ID and apply
// display localization where required; this facade adds no ownership
// check or language resolution.
func (s *Plans) GetPlan(ctx context.Context, id int64) (*SubscriptionPlan, error) {
	return s.store.GetPlan(ctx, id)
}

// CreatePlan prepares a normalized sale template before repository
// creation. Supplied localization source overrides the plain name,
// description, feature text, and product name before required-field
// validation. The repository later prepares localized content and
// validates its revision and translated values.
//
// Legacy GroupID is merged into the positive distinct GroupIDs list,
// and only selected group rates survive normalization. Required
// terms, currency syntax, and quota signs are checked before storage.
// The request passed onward clears the legacy group field and
// contains the same normalized selection used for its rate validation.
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

	// Storage receives one normalized group selection and its validated
	// rate entries. Clear the legacy leading group in that outgoing
	// request so it describes the same selection as the expanded list.
	req.GroupID = 0
	req.GroupIDs = groupIDs
	req.GroupRateMultipliers = groupRates
	req.Currency = currency
	return s.store.CreatePlan(ctx, req)
}

// UpdatePlan prepares selected plan replacements and delegates their
// persistence. Plain text pointers require a Localization update;
// when that update exists, its source supplies all four plain text
// fields before patch validation.
//
// Ordinary omitted pointers and the nullable fields' presence bits
// remain available for the repository to interpret. This service
// checks requested required-field and quota comparisons, while the
// repository normalizes currency and group changes and compares
// localized content revisions. Errors from either phase are returned
// in the phase where they occur.
func (s *Plans) UpdatePlan(ctx context.Context, id int64, req UpdatePlanRequest) (*SubscriptionPlan, error) {
	// Text replacement needs the versioned content update so repository
	// revision checks and translation handling use the submitted source
	// that also supplies the plain compatibility fields.
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

// DeletePlan checks payment's qualifying order count before requesting
// plan deletion. Count failures are wrapped, and a positive count
// returns PENDING_ORDERS. The count and repository delete are separate
// calls here, with no plan lock or transaction spanning them.
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
