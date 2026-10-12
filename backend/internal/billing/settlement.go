package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	// UsageBillingMonetaryScale is the decimal scale for balance and API key monetary counters,
	// matching NUMERIC(20,8) columns such as users.balance and api_keys.quota_used.
	UsageBillingMonetaryScale = 8

	taskCostEpsilon = 0.00000001
)

var (
	ErrTaskInsufficientBalance       = apperror.New(apperror.Category(402), "BATCH_IMAGE_INSUFFICIENT_BALANCE", "insufficient balance for batch image hold")
	ErrTaskNotFound                  = apperror.New(apperror.CategoryNotFound, "BATCH_IMAGE_JOB_NOT_FOUND", "batch image job not found")
	ErrUsageBillingRequestIDRequired = errors.New("usage billing request_id is required")
	ErrUsageBillingRequestConflict   = errors.New("usage billing request fingerprint conflict")

	// ErrTaskSettlementCostExceedsHold reports a capture that exceeds reserved funds.
	ErrTaskSettlementCostExceedsHold = apperror.Conflict("BATCH_IMAGE_SETTLEMENT_COST_EXCEEDS_HOLD", "batch image settlement cost exceeds held balance")

	ErrImageTaskPricingMissing = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_SETTLEMENT_PRICING_MISSING", "batch image settlement pricing is missing")
)

// UsageBillingCommand describes one billable request that must be applied at most once.
type UsageBillingCommand struct {
	RequestID          string
	APIKeyID           int64
	RequestFingerprint string
	RequestPayloadHash string
	// APIKeyBillingMode and PreferredSubscriptionID record the funding choice at gateway admission.
	APIKeyBillingMode       string
	PreferredSubscriptionID *int64

	UserID                          int64
	ActorUserID                     int64
	TeamID                          *int64
	ProviderID                      int64
	GroupID                         *int64
	BillableAmountUSD               float64
	BaseAmountUSD                   float64
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	// Batch reservations can disable plan rate overrides and request base-amount details for later settlement.
	DisablePlanGroupRateMultiplier bool
	IncludeAllocationPricing       bool
	ProviderType                   string
	Model                          string
	ServiceTier                    string
	ReasoningEffort                string
	BillingType                    int8
	InputTokens                    int
	OutputTokens                   int
	CacheCreationTokens            int
	CacheReadTokens                int
	ImageCount                     int
	MediaType                      string

	APIKeyQuotaCost     float64
	APIKeyRateLimitCost float64
	ProviderQuotaCost   float64
}

// ProviderQuotaState holds the post-increment quota state returned by the DB transaction.
// All values are post-update (i.e., already include the increment).
type ProviderQuotaState struct {
	TotalUsed   float64
	TotalLimit  float64
	DailyUsed   float64
	DailyLimit  float64
	WeeklyUsed  float64
	WeeklyLimit float64
}

type UsageBillingApplyResult struct {
	Applied                 bool
	APIKeyQuotaExhausted    bool
	NewBalance              *float64            // post-deduction balance (nil = no balance deduction)
	QuotaState              *ProviderQuotaState // post-increment quota state (nil = no quota increment)
	SubscriptionAmountUSD   float64
	BalanceAmountUSD        float64
	BillingAllocations      []BillingAllocation
	EffectiveRateMultiplier *float64
}

// TaskFundsCommand describes an idempotent task-funding operation.
type TaskFundsCommand struct {
	RequestID          string
	APIKeyID           int64
	RequestFingerprint string
	RequestPayloadHash string
	UserID             int64
	ActorUserID        int64
	TeamID             *int64
	GroupID            *int64
	// APIKeyBillingMode and PreferredSubscriptionID freeze submission-time funding so later key edits cannot redirect settlement.
	APIKeyBillingMode       string
	PreferredSubscriptionID *int64
	Task                    TaskReference
	HoldAmount              float64
	ActualAmount            float64
	// Version 2 snapshots allocate base amounts so subscription and balance funding can use different rates.
	PricingSnapshotVersion          int
	BaseAmountUSD                   float64
	ActualBaseAmountUSD             float64
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	SettlementRateScale             float64
	DisablePlanGroupRateMultiplier  bool
	// BalanceHoldAmount and SubscriptionHoldAllocations are the persisted submission-time holds.
	// When both are absent, legacy tasks treat all of HoldAmount as a balance hold.
	BalanceHoldAmount           float64
	SubscriptionHoldAllocations []BillingAllocation
	// AllowanceReserved indicates whether the task pre-recorded allowance usage, including during rolling upgrades.
	AllowanceReserved bool
	// ReservedAt identifies the original windows when releasing reserved allowance usage.
	ReservedAt time.Time
}

type TaskFundsResult struct {
	Applied               bool
	NewBalance            *float64
	FrozenBalance         *float64
	HoldAmountUSD         float64
	EstimatedAmountUSD    float64
	ActualAmountUSD       float64
	SubscriptionAmountUSD float64
	BalanceAmountUSD      float64
	BillingAllocations    []BillingAllocation
}

// TaskCapturePlan splits actual task costs across held funds and records subscription releases.
type TaskCapturePlan struct {
	BalanceHoldAmount     float64
	ActualAmountUSD       float64
	SubscriptionAmountUSD float64
	BalanceAmountUSD      float64
	BillingAllocations    []BillingAllocation
	SubscriptionReleases  []BillingAllocation
}

// TaskScope identifies a registered task type so funding operations can select its store.
type TaskScope string

// TaskReference carries the task and original reserve request IDs; Scope and ReserveRequestID are excluded from fingerprints.
type TaskReference struct {
	Scope            TaskScope
	ID               string
	ReserveRequestID string
}

func (c *UsageBillingCommand) Normalize() {
	if c == nil {
		return
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	rawMode := strings.TrimSpace(c.APIKeyBillingMode)
	mode, ok := NormalizeAPIKeyBillingMode(c.APIKeyBillingMode)
	if !ok {
		mode = APIKeyBillingModeAuto
	}
	if strings.TrimSpace(c.RequestFingerprint) == "" {
		// Keep an omitted mode empty in the fingerprint for retry compatibility; normalize supplied modes.
		fingerprintCommand := *c
		if rawMode == "" {
			fingerprintCommand.APIKeyBillingMode = ""
		} else {
			fingerprintCommand.APIKeyBillingMode = mode
		}
		if mode != APIKeyBillingModeSubscription {
			fingerprintCommand.PreferredSubscriptionID = nil
		}
		c.RequestFingerprint = buildUsageBillingFingerprint(&fingerprintCommand)
	}
	c.APIKeyBillingMode = mode
	if mode != APIKeyBillingModeSubscription {
		c.PreferredSubscriptionID = nil
	}
	// Compute the fingerprint from raw amounts before quantizing so retries across upgrades
	// do not conflict solely because storage precision changed.
	c.quantizeMonetaryFields()
}

// quantizeMonetaryFields rounds counters written directly to NUMERIC(20,8) columns.
// BillableAmountUSD and BaseAmountUSD retain precision for subscription settlement and usage facts;
// the repository separately rounds the final balance deduction before SQL.
//
// Without prior rounding, PostgreSQL rounds the results of the two operations independently:
//
//	balance    = balance - $1    // Round the remaining balance.
//	quota_used = quota_used + $1 // Round accumulated usage.
//
// NUMERIC rounds ties away from zero. For a ninth-decimal tie, such as
// 10 input tokens * 0.00000125 + 5 output tokens * 0.00001000,
// scaled by 1.25 to 0.000078125, the deltas differ:
//
//	balance:    10000 - 0.000078125 = 9999.999921875 -> 9999.99992188 (delta 0.00007812)
//	quota_used:     0 + 0.000078125 =    0.000078125 ->    0.00007813 (delta 0.00007813)
//
// The 1e-8 difference accumulates across requests and prevents exact reconciliation
// between balance deductions, API key counters, and usage records.
//
// Rounding the amount before SQL puts both operations on the same eight-decimal scale,
// so storing their results does not introduce opposite rounding deltas.
func (c *UsageBillingCommand) quantizeMonetaryFields() {
	c.APIKeyQuotaCost = QuantizeUsageBillingAmount(c.APIKeyQuotaCost)
	c.APIKeyRateLimitCost = QuantizeUsageBillingAmount(c.APIKeyRateLimitCost)
	c.ProviderQuotaCost = QuantizeUsageBillingAmount(c.ProviderQuotaCost)
}

// QuantizeUsageBillingAmount rounds to UsageBillingMonetaryScale decimal places
// using half-away-from-zero rounding, as PostgreSQL NUMERIC does.
//
// Decimal arithmetic avoids the extra binary rounding from math.Round(v*1e8)/1e8
// near a tie. decimal.NewFromFloat uses the float's shortest decimal representation
// before rounding. Zero and nonfinite values pass through unchanged.
func QuantizeUsageBillingAmount(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	quantized, _ := decimal.NewFromFloat(v).Round(UsageBillingMonetaryScale).Float64()
	return quantized
}

func buildUsageBillingFingerprint(c *UsageBillingCommand) string {
	if c == nil {
		return ""
	}
	teamID := int64(0)
	if c.TeamID != nil {
		teamID = *c.TeamID
	}
	preferredSubscriptionID := int64(0)
	if c.PreferredSubscriptionID != nil {
		preferredSubscriptionID = *c.PreferredSubscriptionID
	}
	raw := fmt.Sprintf(
		"%d|%d|%d|%d|%d|%s|%s|%s|%s|%d|%d|%d|%d|%d|%d|%s|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f",
		c.UserID,
		c.ActorUserID,
		teamID,
		c.ProviderID,
		c.APIKeyID,
		strings.TrimSpace(c.ProviderType),
		strings.TrimSpace(c.Model),
		strings.TrimSpace(c.ServiceTier),
		strings.TrimSpace(c.ReasoningEffort),
		c.BillingType,
		c.InputTokens,
		c.OutputTokens,
		c.CacheCreationTokens,
		c.CacheReadTokens,
		c.ImageCount,
		strings.TrimSpace(c.MediaType),
		c.BillableAmountUSD,
		c.BaseAmountUSD,
		c.SubscriptionRateMultiplier,
		c.SubscriptionRateMultiplierScale,
		c.BalanceRateMultiplier,
		c.APIKeyQuotaCost,
		c.APIKeyRateLimitCost,
		c.ProviderQuotaCost,
	)
	raw += fmt.Sprintf("|%s|%d", c.APIKeyBillingMode, preferredSubscriptionID)
	if payloadHash := strings.TrimSpace(c.RequestPayloadHash); payloadHash != "" {
		raw += "|" + payloadHash
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func HashUsageRequestPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (c *TaskFundsCommand) Normalize() {
	if c == nil {
		return
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	c.Task.ID = strings.TrimSpace(c.Task.ID)
	rawMode := strings.TrimSpace(c.APIKeyBillingMode)
	mode, ok := NormalizeAPIKeyBillingMode(c.APIKeyBillingMode)
	if !ok {
		mode = APIKeyBillingModeAuto
	}
	if strings.TrimSpace(c.RequestFingerprint) == "" {
		// Keep an omitted mode empty for versioned fingerprint compatibility; normalize supplied modes.
		fingerprintCommand := *c
		if rawMode == "" {
			fingerprintCommand.APIKeyBillingMode = ""
		} else {
			fingerprintCommand.APIKeyBillingMode = mode
		}
		if mode != APIKeyBillingModeSubscription {
			fingerprintCommand.PreferredSubscriptionID = nil
		}
		c.RequestFingerprint = buildTaskFundsFingerprint(&fingerprintCommand)
	}
	c.APIKeyBillingMode = mode
	if mode != APIKeyBillingModeSubscription {
		c.PreferredSubscriptionID = nil
	}
}

func buildTaskFundsFingerprint(c *TaskFundsCommand) string {
	if c == nil {
		return ""
	}
	teamID := int64(0)
	if c.TeamID != nil {
		teamID = *c.TeamID
	}
	var raw string
	if c.PricingSnapshotVersion >= 2 {
		raw = fmt.Sprintf(
			"%d|%d|%d|%d|%s|%d|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%t|%s",
			c.UserID,
			c.ActorUserID,
			teamID,
			c.APIKeyID,
			strings.TrimSpace(c.Task.ID),
			c.PricingSnapshotVersion,
			c.BaseAmountUSD,
			c.ActualBaseAmountUSD,
			c.SubscriptionRateMultiplier,
			c.SubscriptionRateMultiplierScale,
			c.BalanceRateMultiplier,
			c.SettlementRateScale,
			c.DisablePlanGroupRateMultiplier,
			c.ReservedAt.UTC().Format(time.RFC3339Nano),
		)
	} else {
		raw = fmt.Sprintf(
			"%d|%d|%d|%d|%s|%0.10f|%0.10f|%s",
			c.UserID,
			c.ActorUserID,
			teamID,
			c.APIKeyID,
			strings.TrimSpace(c.Task.ID),
			c.HoldAmount,
			c.ActualAmount,
			c.ReservedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	if c.PricingSnapshotVersion >= 3 {
		preferredSubscriptionID := int64(0)
		if c.PreferredSubscriptionID != nil {
			preferredSubscriptionID = *c.PreferredSubscriptionID
		}
		raw += fmt.Sprintf("|%s|%d", c.APIKeyBillingMode, preferredSubscriptionID)
	}
	if payloadHash := strings.TrimSpace(c.RequestPayloadHash); payloadHash != "" {
		raw += "|" + payloadHash
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// EffectiveTaskBalanceHoldAmount returns the balance hold, treating legacy unsplit holds as entirely balance-funded.
func EffectiveTaskBalanceHoldAmount(cmd *TaskFundsCommand) float64 {
	if cmd == nil {
		return 0
	}
	if cmd.BalanceHoldAmount > 0 || len(cmd.SubscriptionHoldAllocations) > 0 {
		return math.Max(cmd.BalanceHoldAmount, 0)
	}
	return math.Max(cmd.HoldAmount, 0)
}

// TotalTaskHoldAmount sums positive subscription holds and the effective balance hold.
func TotalTaskHoldAmount(cmd *TaskFundsCommand) float64 {
	if cmd == nil {
		return 0
	}
	total := EffectiveTaskBalanceHoldAmount(cmd)
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type == BillingAllocationTypeSubscription && allocation.AmountUSD > 0 {
			total += allocation.AmountUSD
		}
	}
	return total
}

// PlanTaskCapture allocates actual costs to subscriptions first and computes unused subscription releases.
func PlanTaskCapture(cmd *TaskFundsCommand) (*TaskCapturePlan, error) {
	plan := &TaskCapturePlan{}
	if cmd == nil {
		return plan, nil
	}
	if cmd.PricingSnapshotVersion >= 2 && cmd.BaseAmountUSD > 0 {
		return planTaskBaseAmountCapture(cmd)
	}
	actualAmount := math.Max(cmd.ActualAmount, 0)
	remaining := actualAmount
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type != BillingAllocationTypeSubscription || allocation.AmountUSD <= 0 || allocation.SubscriptionID == nil {
			continue
		}
		kept := math.Min(allocation.AmountUSD, remaining)
		if kept > 0 {
			keptAllocation := CloneBillingAllocation(allocation, kept)
			plan.BillingAllocations = append(plan.BillingAllocations, keptAllocation)
			plan.SubscriptionAmountUSD += kept
			remaining -= kept
		}
		if released := allocation.AmountUSD - kept; released > taskCostEpsilon {
			plan.SubscriptionReleases = append(plan.SubscriptionReleases, CloneBillingAllocation(allocation, released))
		}
	}

	plan.BalanceHoldAmount = EffectiveTaskBalanceHoldAmount(cmd)
	if remaining-plan.BalanceHoldAmount > taskCostEpsilon {
		return nil, ErrTaskSettlementCostExceedsHold
	}
	plan.BalanceAmountUSD = math.Min(remaining, plan.BalanceHoldAmount)
	if plan.BalanceAmountUSD > 0 {
		plan.BillingAllocations = append(plan.BillingAllocations, BillingAllocation{
			Type:      BillingAllocationTypeBalance,
			AmountUSD: plan.BalanceAmountUSD,
		})
	}
	plan.ActualAmountUSD = plan.SubscriptionAmountUSD + plan.BalanceAmountUSD
	return plan, nil
}

// planTaskBaseAmountCapture covers actual base cost using the rates recorded for each reserved source.
func planTaskBaseAmountCapture(cmd *TaskFundsCommand) (*TaskCapturePlan, error) {
	plan := &TaskCapturePlan{BalanceHoldAmount: EffectiveTaskBalanceHoldAmount(cmd)}
	remainingBase := math.Max(cmd.ActualBaseAmountUSD, 0)
	settlementScale := math.Max(cmd.SettlementRateScale, 0)
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type != BillingAllocationTypeSubscription || allocation.AmountUSD <= 0 || allocation.SubscriptionID == nil {
			continue
		}
		rate := allocation.RateMultiplier * settlementScale
		if rate <= 0 {
			continue
		}
		kept := math.Min(allocation.AmountUSD, remainingBase*rate)
		if kept > 0 {
			keptAllocation := CloneBillingAllocation(allocation, kept)
			keptAllocation.BaseAmountUSD = kept / rate
			keptAllocation.RateMultiplier = rate
			plan.BillingAllocations = append(plan.BillingAllocations, keptAllocation)
			plan.SubscriptionAmountUSD += kept
			remainingBase -= kept / rate
		}
		if released := allocation.AmountUSD - kept; released > taskCostEpsilon {
			plan.SubscriptionReleases = append(plan.SubscriptionReleases, CloneBillingAllocation(allocation, released))
		}
	}

	balanceRate := math.Max(cmd.BalanceRateMultiplier, 0) * settlementScale
	balanceAmount := remainingBase * balanceRate
	if balanceAmount-plan.BalanceHoldAmount > taskCostEpsilon {
		return nil, ErrTaskSettlementCostExceedsHold
	}
	plan.BalanceAmountUSD = math.Min(balanceAmount, plan.BalanceHoldAmount)
	if plan.BalanceAmountUSD > 0 {
		baseAmount := 0.0
		if balanceRate > 0 {
			baseAmount = plan.BalanceAmountUSD / balanceRate
		}
		plan.BillingAllocations = append(plan.BillingAllocations, BillingAllocation{
			Type:           BillingAllocationTypeBalance,
			AmountUSD:      plan.BalanceAmountUSD,
			BaseAmountUSD:  baseAmount,
			RateMultiplier: balanceRate,
		})
		remainingBase -= baseAmount
	}
	if remainingBase > taskCostEpsilon && balanceRate > 0 {
		return nil, ErrTaskSettlementCostExceedsHold
	}
	plan.ActualAmountUSD = plan.SubscriptionAmountUSD + plan.BalanceAmountUSD
	return plan, nil
}

func CloneBillingAllocation(allocation BillingAllocation, amount float64) BillingAllocation {
	cloned := allocation
	cloned.AmountUSD = amount
	if allocation.SubscriptionID != nil {
		value := *allocation.SubscriptionID
		cloned.SubscriptionID = &value
	}
	if allocation.PlanID != nil {
		value := *allocation.PlanID
		cloned.PlanID = &value
	}
	return cloned
}
