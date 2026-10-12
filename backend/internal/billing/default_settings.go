package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var (
	ErrDefaultSubPlanInvalid = apperror.BadRequest(
		"DEFAULT_SUBSCRIPTION_PLAN_INVALID",
		"default subscription plan must exist",
	)

	ErrDefaultSubPlanDuplicate = apperror.BadRequest(
		"DEFAULT_SUBSCRIPTION_PLAN_DUPLICATE",
		"default subscription plan cannot be duplicated",
	)
)

// DefaultSubscriptionSetting identifies a plan granted by the default entitlement settings.
type DefaultSubscriptionSetting struct {
	PlanID int64 `json:"plan_id"`
}

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

// ParseDefaultSubscriptions returns nil for invalid JSON and removes nonpositive plan IDs.
func ParseDefaultSubscriptions(raw string) []DefaultSubscriptionSetting {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var items []DefaultSubscriptionSetting
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
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
