package creative

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

const (
	// Billing uses these request ID prefixes with usage_billing_dedup so retries of
	// the same hold, capture, or release for a run do not repeat the funds operation.
	creativeHoldRequestPrefix       = "creative_hold:"
	creativeCaptureRequestPrefix    = "creative_capture:"
	creativeReleaseRequestPrefix    = "creative_release:"
	creativeSettlementRequestPrefix = "creative_settle:"

	// creativePricingSnapshotVersion identifies snapshots allocated by base amount.
	// Creative uses scale 1 with the same reservation and settlement price, without batch discounts.
	creativePricingSnapshotVersion = 2

	// FundingScope identifies funds reserved for creative runs.
	FundingScope billing.TaskScope = "creative"
)

// FundingStore reserves, captures, and releases run funds.
type FundingStore interface {
	Reserve(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
	Capture(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
	Release(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
}

type Funding struct {
	Store   FundingStore
	Observe func(string, ...any)
}

func CreativeHoldRequestID(runID string) string {
	return creativeHoldRequestPrefix + strings.TrimSpace(runID)
}

func CreativeCaptureRequestID(runID string) string {
	return creativeCaptureRequestPrefix + strings.TrimSpace(runID)
}

func CreativeReleaseRequestID(runID string) string {
	return creativeReleaseRequestPrefix + strings.TrimSpace(runID)
}

func CreativeSettlementRequestID(runID string) string {
	return creativeSettlementRequestPrefix + strings.TrimSpace(runID)
}

// BuildHoldCommand converts run metadata into a billing.TaskFundsCommand.
// The Task field identifies the creative run and its reservation request.
func BuildHoldCommand(run *CreativeRun, requestID string, actualBaseAmount float64) (*billing.TaskFundsCommand, error) {
	if run == nil {
		return nil, ErrCreativeBillingHoldFailed
	}
	if run.APIKeyID <= 0 {
		return nil, ErrCreativeBillingHoldFailed
	}
	holdAmount := run.EstimatedCost
	if run.HoldAmount != nil {
		holdAmount = *run.HoldAmount
	}
	if holdAmount < 0 {
		holdAmount = 0
	}
	if actualBaseAmount < 0 {
		actualBaseAmount = 0
	}
	groupID := run.GroupID
	cmd := &billing.TaskFundsCommand{
		RequestID:                       requestID,
		APIKeyID:                        run.APIKeyID,
		UserID:                          run.UserID,
		ActorUserID:                     run.UserID,
		GroupID:                         &groupID,
		Task:                            FundingReference(run.RunID),
		APIKeyBillingMode:               billing.APIKeyBillingModeAuto,
		HoldAmount:                      holdAmount,
		ActualAmount:                    0,
		PricingSnapshotVersion:          creativePricingSnapshotVersion,
		BaseAmountUSD:                   math.Max(run.BaseUnitPrice, 0) * float64(max(run.RequestedOutputCount, 0)),
		ActualBaseAmountUSD:             actualBaseAmount,
		SubscriptionRateMultiplier:      math.Max(run.SubscriptionRateMultiplier, 0),
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           math.Max(run.BalanceRateMultiplier, 0),
		SettlementRateScale:             1,
		DisablePlanGroupRateMultiplier:  !run.PlanGroupRateEnabled,
		BalanceHoldAmount:               run.BalanceHoldAmount,
		SubscriptionHoldAllocations:     cloneBillingAllocations(run.SubscriptionHoldAllocations),
		AllowanceReserved:               run.AllowanceReserved,

		ReservedAt:         run.CreatedAt,
		RequestPayloadHash: strings.TrimSpace(run.RequestFingerprint),
	}
	return cmd, nil
}

// Reserve reserves the estimated cost and updates the run funding snapshot on success.
func (funding Funding) Reserve(ctx context.Context, run *CreativeRun) error {
	if funding.Store == nil {
		return ErrCreativeBillingHoldFailed.WithCause(errors.New("creative billing repository is not configured"))
	}
	if run.EstimatedCost <= 0 {
		// Zero-cost runs need no reservation.
		run.BalanceHoldAmount = 0
		run.SubscriptionHoldAllocations = nil
		return nil
	}
	cmd, err := BuildHoldCommand(run, CreativeHoldRequestID(run.RunID), 0)
	if err != nil {
		return err
	}
	// Reservation counts toward key and member quotas and persists that fact on the run.
	// Capture and release use the persisted run.AllowanceReserved flag.
	cmd.AllowanceReserved = true
	result, err := funding.Store.Reserve(ctx, cmd)
	if err != nil {
		if errors.Is(err, billing.ErrTaskInsufficientBalance) {
			return ErrCreativeInsufficientBalance
		}
		if errors.Is(err, billing.ErrAPIKeyQuotaExhausted) || errors.Is(err, billing.ErrAPIKeyRateLimit5hExceeded) || errors.Is(err, billing.ErrAPIKeyRateLimit1dExceeded) ||
			errors.Is(err, billing.ErrAPIKeyRateLimit7dExceeded) || errors.Is(err, billing.ErrTeamMemberDailyExceeded) || errors.Is(err, billing.ErrTeamMemberWeeklyExceeded) ||
			errors.Is(err, billing.ErrTeamMemberMonthlyExceeded) {
			return err
		}
		return ErrCreativeBillingHoldFailed.WithCause(err)
	}
	if result != nil {
		run.BalanceHoldAmount = result.BalanceAmountUSD
		run.SubscriptionHoldAllocations = batchImageSubscriptionAllocations(result.BillingAllocations)
		holdAmount := result.HoldAmountUSD
		run.HoldAmount = &holdAmount
		run.EstimatedCost = result.EstimatedAmountUSD
	}
	// Update the in-memory flag so creation rollback uses the reserved allowance state.
	run.AllowanceReserved = true
	return nil
}

// Capture charges successful outputs, deduplicated by request ID.
func (funding Funding) Capture(ctx context.Context, run *CreativeRun, successCount int) (*billing.TaskFundsResult, error) {
	if funding.Store == nil {
		return nil, ErrCreativeSettlementBillingFail.WithCause(errors.New("creative billing repository is not configured"))
	}
	actualBase := math.Max(run.BaseUnitPrice, 0) * float64(max(successCount, 0))
	cmd, err := BuildHoldCommand(run, CreativeCaptureRequestID(run.RunID), actualBase)
	if err != nil {
		return nil, err
	}
	result, err := funding.Store.Capture(ctx, cmd)
	if err != nil {
		return nil, ErrCreativeSettlementBillingFail.WithCause(err)
	}
	if result != nil {
		run.ActualCost = &result.ActualAmountUSD
	}
	return result, nil
}

// Release returns unused reserved funds for failed, cancelled, or lost-result runs.
// A request ID fingerprint conflict is treated as an already completed release.
func (funding Funding) Release(ctx context.Context, run *CreativeRun) error {
	if funding.Store == nil || run == nil {
		return nil
	}
	holdAmount := run.EstimatedCost
	if run.HoldAmount != nil {
		holdAmount = *run.HoldAmount
	}
	if holdAmount <= 0 {
		return nil
	}
	cmd, err := BuildHoldCommand(run, CreativeReleaseRequestID(run.RunID), 0)
	if err != nil {
		return err
	}
	if _, err := funding.Store.Release(ctx, cmd); err != nil {
		if errors.Is(err, billing.ErrUsageBillingRequestConflict) {
			if funding.Observe != nil {
				funding.Observe("creative.release_fingerprint_conflict_treated_as_released", "run_id", run.RunID)
			}
			return nil
		}
		return ErrCreativeBillingHoldFailed.WithCause(err)
	}
	return nil
}

func cloneBillingAllocations(values []billing.BillingAllocation) []billing.BillingAllocation {
	if values == nil {
		return nil
	}
	out := make([]billing.BillingAllocation, len(values))
	for i, v := range values {
		out[i] = billing.CloneBillingAllocation(v, v.AmountUSD)
	}
	return out
}

func batchImageSubscriptionAllocations(values []billing.BillingAllocation) []billing.BillingAllocation {
	out := make([]billing.BillingAllocation, 0, len(values))
	for _, v := range values {
		if v.Type == billing.BillingAllocationTypeSubscription && v.AmountUSD > 0 && v.SubscriptionID != nil {
			out = append(out, billing.CloneBillingAllocation(v, v.AmountUSD))
		}
	}
	return out
}

// FundingReference returns the run funding reference and reservation request ID.
func FundingReference(id string) billing.TaskReference {
	id = strings.TrimSpace(id)
	return billing.TaskReference{Scope: FundingScope, ID: id, ReserveRequestID: CreativeHoldRequestID(id)}
}
