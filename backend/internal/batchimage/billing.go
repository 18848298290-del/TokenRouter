package batchimage

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

const (
	batchImageHoldRequestPrefix    = "batch_image_hold:"
	batchImageCaptureRequestPrefix = "batch_image_capture:"
	batchImageReleaseRequestPrefix = "batch_image_release:"

	// FundingScope identifies funds reserved for batch image jobs.
	FundingScope billing.TaskScope = "batchimage"
)

// FundingStore uses billing.Funds for reserve, capture, and release with stable request IDs per job.
type FundingStore interface {
	Reserve(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
	Capture(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
	Release(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error)
}
type Funding struct {
	Store   FundingStore
	Observe func(string, ...any)
}

func BatchImageHoldRequestID(batchID string) string {
	return batchImageHoldRequestPrefix + strings.TrimSpace(batchID)
}

func BatchImageCaptureRequestID(batchID string) string {
	return batchImageCaptureRequestPrefix + strings.TrimSpace(batchID)
}

func BatchImageReleaseRequestID(batchID string) string {
	return batchImageReleaseRequestPrefix + strings.TrimSpace(batchID)
}

// BillingUserID returns the payer, using the owner for older jobs without a payer snapshot.
func BillingUserID(job *BatchImageJob) int64 {
	if job == nil {
		return 0
	}
	if job.BillingUserID > 0 {
		return job.BillingUserID
	}
	return job.UserID
}

func BuildHoldCommand(job *BatchImageJob, requestID string, actualAmount float64, payloadHash string) (*billing.TaskFundsCommand, error) {
	if job == nil {
		return nil, ErrBatchImageBillingHoldFailed
	}
	if job.APIKeyID == nil || *job.APIKeyID <= 0 {
		return nil, ErrBatchImageSettlementMissingAPIKeyID
	}
	holdAmount := job.EstimatedCost
	if job.HoldAmount != nil {
		holdAmount = *job.HoldAmount
	}
	if holdAmount < 0 {
		holdAmount = 0
	}
	if actualAmount < 0 {
		actualAmount = 0
	}
	billingUserID := BillingUserID(job)
	command := &billing.TaskFundsCommand{
		RequestID:                   requestID,
		APIKeyID:                    *job.APIKeyID,
		UserID:                      billingUserID,
		ActorUserID:                 job.UserID,
		TeamID:                      job.TeamID,
		Task:                        FundingReference(job.BatchID),
		APIKeyBillingMode:           job.BillingMode,
		PreferredSubscriptionID:     CloneInt64Ptr(job.PreferredSubscriptionID),
		HoldAmount:                  holdAmount,
		ActualAmount:                actualAmount,
		BalanceHoldAmount:           job.BalanceHoldAmount,
		SubscriptionHoldAllocations: cloneBillingAllocations(job.SubscriptionHoldAllocations),
		AllowanceReserved:           job.AllowanceReserved,
		ReservedAt:                  job.CreatedAt,
		RequestPayloadHash:          strings.TrimSpace(payloadHash),
	}
	if job.PricingSnapshotVersion >= 2 {
		scale := math.Max(job.ProviderRateMultiplier, 0) * math.Max(job.HoldMultiplier, 0)
		settlementScale := 0.0
		if job.HoldMultiplier > 0 {
			settlementScale = math.Max(job.BatchDiscountMultiplier, 0) / job.HoldMultiplier
		}
		command.PricingSnapshotVersion = job.PricingSnapshotVersion
		command.BaseAmountUSD = math.Max(job.BaseUnitPrice, 0) * float64(max(job.ItemCount, 0))
		command.ActualBaseAmountUSD = math.Max(actualAmount, 0)
		command.ActualAmount = 0
		command.SubscriptionRateMultiplier = math.Max(job.SubscriptionRateMultiplier, 0) * scale
		command.SubscriptionRateMultiplierScale = scale
		command.BalanceRateMultiplier = math.Max(job.BalanceRateMultiplier, 0) * scale
		command.SettlementRateScale = settlementScale
		command.DisablePlanGroupRateMultiplier = !job.PlanGroupRateEnabled
	}
	return command, nil
}

func (f Funding) Reserve(ctx context.Context, job *BatchImageJob, groupID *int64, payloadHash string) error {
	if f.Store == nil {
		return ErrBatchImageBillingHoldFailed.WithCause(errors.New("batch image billing repository is not configured"))
	}
	cmd, err := BuildHoldCommand(job, BatchImageHoldRequestID(job.BatchID), 0, payloadHash)
	if err != nil {
		return err
	}
	if cmd.HoldAmount <= 0 && cmd.BaseAmountUSD <= 0 {
		return nil
	}
	cmd.GroupID = CloneInt64Ptr(groupID)
	result, err := f.Store.Reserve(ctx, cmd)
	if err != nil {
		if errors.Is(err, ErrBatchImageInsufficientBalance) {
			return ErrBatchImageInsufficientBalance
		}
		// A selected subscription is the required funding source; return reservation eligibility errors unchanged.
		if errors.Is(err, billing.ErrPreferredSubscriptionInvalid) || errors.Is(err, billing.ErrPreferredSubscriptionGroup) || errors.Is(err, billing.ErrPreferredSubscriptionInsufficient) {
			return err
		}
		if errors.Is(err, billing.ErrAPIKeyQuotaExhausted) || errors.Is(err, billing.ErrAPIKeyRateLimit5hExceeded) || errors.Is(err, billing.ErrAPIKeyRateLimit1dExceeded) || errors.Is(err, billing.ErrAPIKeyRateLimit7dExceeded) ||
			errors.Is(err, billing.ErrTeamMemberDailyExceeded) || errors.Is(err, billing.ErrTeamMemberWeeklyExceeded) || errors.Is(err, billing.ErrTeamMemberMonthlyExceeded) {
			return err
		}
		return ErrBatchImageBillingHoldFailed.WithCause(err)
	}
	if result != nil {
		job.BalanceHoldAmount = result.BalanceAmountUSD
		job.SubscriptionHoldAllocations = SubscriptionAllocations(result.BillingAllocations)
		if job.PricingSnapshotVersion >= 2 {
			holdAmount := result.HoldAmountUSD
			job.HoldAmount = &holdAmount
			job.EstimatedCost = result.EstimatedAmountUSD
		}
	}
	job.AllowanceReserved = true
	return nil
}

func (f Funding) Capture(ctx context.Context, job *BatchImageJob, actualAmount float64, payloadHash string) (*billing.TaskFundsResult, error) {
	if f.Store == nil {
		return nil, ErrBatchImageSettlementBillingFailed.WithCause(errors.New("batch image billing repository is not configured"))
	}
	cmd, err := BuildHoldCommand(job, BatchImageCaptureRequestID(job.BatchID), actualAmount, payloadHash)
	if err != nil {
		return nil, err
	}
	result, err := f.Store.Capture(ctx, cmd)
	if err != nil {
		return nil, ErrBatchImageSettlementBillingFailed.WithCause(err)
	}
	job.AllowanceReserved = false
	return result, nil
}

func (f Funding) Release(ctx context.Context, job *BatchImageJob, payloadHash string) error {
	if f.Store == nil || job == nil {
		return nil
	}
	cmd, err := BuildHoldCommand(job, BatchImageReleaseRequestID(job.BatchID), 0, payloadHash)
	if err != nil {
		return err
	}
	if cmd.HoldAmount <= 0 {
		return nil
	}
	if _, err := f.Store.Release(ctx, cmd); err != nil {
		// A fingerprint conflict means this release request ID already committed with a different
		// payloadHash and returned the funds. Treat it as success so historical fingerprint
		// mismatches do not leave the job retrying release indefinitely.
		if errors.Is(err, billing.ErrUsageBillingRequestConflict) {
			f.warn("batch_image.release_fingerprint_conflict_treated_as_released",
				"batch_id", job.BatchID,
			)
			return nil
		}
		return ErrBatchImageBillingHoldFailed.WithCause(err)
	}
	job.AllowanceReserved = false
	return nil
}

func SubscriptionAllocations(allocations []billing.BillingAllocation) []billing.BillingAllocation {
	result := make([]billing.BillingAllocation, 0, len(allocations))
	for _, allocation := range allocations {
		if allocation.Type != billing.BillingAllocationTypeSubscription || allocation.AmountUSD <= 0 || allocation.SubscriptionID == nil {
			continue
		}
		result = append(result, billing.CloneBillingAllocation(allocation, allocation.AmountUSD))
	}
	return result
}

func CloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (f Funding) warn(event string, values ...any) {
	if f.Observe != nil {
		f.Observe(event, values...)
	}
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

// FundingReference returns the job funding reference and reservation request ID.
func FundingReference(id string) billing.TaskReference {
	id = strings.TrimSpace(id)
	return billing.TaskReference{Scope: FundingScope, ID: id, ReserveRequestID: "batch_image_hold:" + id}
}
