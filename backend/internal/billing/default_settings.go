package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Default subscription selections are stored as plan-ID JSON and consumed
// during signup. Reads omit nonpositive IDs and tolerate invalid JSON, while
// settings validation rejects repeated positive IDs and verifies plans when a
// lookup is supplied.

var (
	// These validation errors distinguish missing default grant plans from
	// duplicated selections so settings updates can identify the offending plan
	// ID.
	ErrDefaultSubPlanInvalid = apperror.BadRequest(
		"DEFAULT_SUBSCRIPTION_PLAN_INVALID",
		"default subscription plan must exist",
	)

	// Repeated positive plan selections are rejected before a settings update can
	// grant the same signup plan more than once.
	ErrDefaultSubPlanDuplicate = apperror.BadRequest(
		"DEFAULT_SUBSCRIPTION_PLAN_DUPLICATE",
		"default subscription plan cannot be duplicated",
	)
)

// DefaultSubscriptionSetting identifies a plan granted by the default entitlement settings.
type DefaultSubscriptionSetting struct {
	PlanID int64 `json:"plan_id"` // signup grant plan identifier
}

// ValidateDefaultSubscriptionPlans checks each positive plan ID for
// duplication and, when lookup is supplied, existence. Nonpositive IDs are
// ignored; a nil lookup still checks duplicates, and repository errors other
// than not-found retain their cause.
func ValidateDefaultSubscriptionPlans(ctx context.Context, items []DefaultSubscriptionSetting, lookup func(context.Context, int64) (*SubscriptionPlan, error)) error {
	if len(items) == 0 {
		return nil
	}

	checked := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item.PlanID <= 0 {
			continue
		}
		if _, ok := checked[item.PlanID]; ok {
			return ErrDefaultSubPlanDuplicate.WithMetadata(map[string]string{
				"plan_id": strconv.FormatInt(item.PlanID, 10),
			})
		}
		checked[item.PlanID] = struct{}{}
		if lookup == nil {
			continue
		}

		plan, err := lookup(ctx, item.PlanID)
		if err != nil {
			if apperror.IsNotFound(err) {
				return ErrDefaultSubPlanInvalid.WithMetadata(map[string]string{
					"plan_id": strconv.FormatInt(item.PlanID, 10),
				})
			}
			return fmt.Errorf("get default subscription plan %d: %w", item.PlanID, err)
		}
		if plan == nil || plan.ID <= 0 {
			return ErrDefaultSubPlanInvalid.WithMetadata(map[string]string{
				"plan_id": strconv.FormatInt(item.PlanID, 10),
			})
		}
	}

	return nil
}

// ParseDefaultSubscriptions returns positive plan selections in their supplied
// order. Empty or invalid JSON yields nil, while duplicate positive IDs
// survive parsing and are rejected separately by settings validation.
func ParseDefaultSubscriptions(raw string) []DefaultSubscriptionSetting {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var items []DefaultSubscriptionSetting
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		// Undecodable saved selections produce no signup grants; administrator
		// validation and persistence handle correcting the stored value separately.
		return nil
	}

	normalized := make([]DefaultSubscriptionSetting, 0, len(items))
	for _, item := range items {
		if item.PlanID <= 0 {
			continue
		}
		normalized = append(normalized, item)
	}

	return normalized
}
