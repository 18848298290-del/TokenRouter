package completion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// SettlementInput contains calculated charges and identities for settlement and its committed effects.
type SettlementInput struct {
	Cost                                                                                                       *CostBreakdown
	User                                                                                                       *PayerSnapshot
	APIKey                                                                                                     *KeySnapshot
	Provider                                                                                                   *ProviderSnapshot
	Subscription                                                                                               *billing.UserSubscription
	RequestPayloadHash                                                                                         string
	ProviderRateMultiplier, SubscriptionRateMultiplier, SubscriptionRateMultiplierScale, BalanceRateMultiplier float64
	QuotaUpdates                                                                                               bool
	BillingBaseAmountUSD                                                                                       *float64
}
type usageBillingParams = SettlementInput

func (p *SettlementInput) shouldDeductAPIKeyQuota() bool {
	return p.Cost.ActualCost > 0 && p.APIKey.Quota > 0 && p.QuotaUpdates
}

func (p *SettlementInput) shouldUpdateRateLimits() bool {
	return p.Cost.ActualCost > 0 && p.APIKey.HasRateLimits && p.QuotaUpdates
}

func (p *SettlementInput) shouldUpdateProviderQuota(cost float64) bool {
	return cost > 0 && p.Provider.QuotaEligible && p.Provider.HasQuotaLimit
}

func BuildCommand(requestID string, usageLog *UsageLog, p *usageBillingParams) *billing.UsageBillingCommand {
	if p == nil || p.Cost == nil || p.APIKey == nil || p.User == nil || p.Provider == nil {
		return nil
	}

	cmd := &billing.UsageBillingCommand{
		RequestID:          requestID,
		APIKeyID:           p.APIKey.ID,
		APIKeyBillingMode:  p.APIKey.BillingMode,
		UserID:             p.User.ID,
		ActorUserID:        p.User.ID,
		ProviderID:         p.Provider.ID,
		ProviderType:       p.Provider.Type,
		RequestPayloadHash: strings.TrimSpace(p.RequestPayloadHash),
	}
	if p.APIKey.PreferredSubscriptionID != nil {
		preferredSubscriptionID := *p.APIKey.PreferredSubscriptionID
		cmd.PreferredSubscriptionID = &preferredSubscriptionID
	}
	if p.APIKey.ActorUserPresent || p.APIKey.ActorUserID != 0 {
		cmd.ActorUserID = p.APIKey.ActorUserID
	}
	if p.APIKey.TeamID != nil {
		teamID := *p.APIKey.TeamID
		cmd.TeamID = &teamID
	}
	if p.APIKey.GroupID != nil && *p.APIKey.GroupID > 0 {
		groupID := *p.APIKey.GroupID
		cmd.GroupID = &groupID
	}
	if usageLog != nil {
		cmd.Model = usageLog.Model
		cmd.BillingType = usageLog.BillingType
		cmd.InputTokens = usageLog.InputTokens
		cmd.OutputTokens = usageLog.OutputTokens
		cmd.CacheCreationTokens = usageLog.CacheCreationTokens
		cmd.CacheReadTokens = usageLog.CacheReadTokens
		cmd.ImageCount = usageLog.ImageCount
		if usageLog.ServiceTier != nil {
			cmd.ServiceTier = *usageLog.ServiceTier
		}
		if usageLog.ReasoningEffort != nil {
			cmd.ReasoningEffort = *usageLog.ReasoningEffort
		}
	}

	if p.Cost.ActualCost > 0 {
		cmd.BillableAmountUSD = p.Cost.ActualCost
	}
	baseAmount := p.Cost.TotalCost
	if p.BillingBaseAmountUSD != nil {
		baseAmount = *p.BillingBaseAmountUSD
	}
	if baseAmount > 0 {
		cmd.BaseAmountUSD = baseAmount
		ApplyRateMultipliers(cmd, p)
	}
	if p.shouldDeductAPIKeyQuota() {
		cmd.APIKeyQuotaCost = p.Cost.ActualCost
	}
	if p.shouldUpdateRateLimits() {
		cmd.APIKeyRateLimitCost = p.Cost.ActualCost
	}
	providerQuotaCost := ProviderQuotaCost(usageLog, p)
	if p.shouldUpdateProviderQuota(providerQuotaCost) {
		cmd.ProviderQuotaCost = providerQuotaCost
	}

	cmd.Normalize()
	return cmd
}

func ProviderQuotaCost(usageLog *UsageLog, p *usageBillingParams) float64 {
	if p == nil || p.Cost == nil {
		return 0
	}
	baseCost := p.Cost.TotalCost
	if usageLog != nil && usageLog.ProviderStatsCost != nil {
		baseCost = *usageLog.ProviderStatsCost
	}
	if baseCost <= 0 || p.ProviderRateMultiplier <= 0 {
		return 0
	}
	return baseCost * p.ProviderRateMultiplier
}

func ApplyRateMultipliers(cmd *billing.UsageBillingCommand, p *usageBillingParams) {
	if cmd == nil || p == nil || p.Cost == nil {
		return
	}
	baseAmount := p.Cost.TotalCost
	if p.BillingBaseAmountUSD != nil {
		baseAmount = *p.BillingBaseAmountUSD
	}
	if baseAmount <= 0 {
		return
	}

	effectiveRate := p.Cost.ActualCost / baseAmount

	cmd.SubscriptionRateMultiplier = RateOrFallback(p.SubscriptionRateMultiplier, effectiveRate)
	cmd.SubscriptionRateMultiplierScale = p.SubscriptionRateMultiplierScale
	if cmd.SubscriptionRateMultiplierScale <= 0 {
		cmd.SubscriptionRateMultiplierScale = 1
	}
	cmd.BalanceRateMultiplier = RateOrFallback(p.BalanceRateMultiplier, effectiveRate)
}

func (s *Recorder) Apply(ctx context.Context, requestID string, usageLog *UsageLog, p *usageBillingParams) (bool, error) {
	if p == nil || s.effects == nil {
		return false, nil
	}

	cmd := BuildCommand(requestID, usageLog, p)
	if s.store == nil {
		return false, fmt.Errorf("usage billing repository is required")
	}
	if cmd == nil || cmd.RequestID == "" {
		return false, fmt.Errorf("usage billing command is invalid")
	}

	billingCtx, cancel := detachedBillingContext(ctx)
	defer cancel()

	result, err := s.store.Apply(billingCtx, cmd)
	if err != nil {
		return false, err
	}

	if result == nil || !result.Applied {
		s.effects.ProviderUsed(p.Provider.ID)
		return false, nil
	}

	ApplyResultToLog(usageLog, result)
	if result.APIKeyQuotaExhausted {
		if p.QuotaUpdates && p.APIKey != nil && p.APIKey.Key != "" {
			s.effects.InvalidateAuth(billingCtx, p.APIKey.Key)
		}
	}

	s.effects.Settled(*p, result)
	return true, nil
}

func detachedBillingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 15*time.Second)
}

func ApplyResultToLog(usageLog *UsageLog, result *billing.UsageBillingApplyResult) {
	if usageLog == nil || result == nil {
		return
	}

	usageLog.SubscriptionAmountUSD = result.SubscriptionAmountUSD
	usageLog.BalanceAmountUSD = result.BalanceAmountUSD
	usageLog.BillingAllocations = cloneBillingAllocations(result.BillingAllocations)
	usageLog.SubscriptionID = firstAllocatedSubscriptionID(result.BillingAllocations)
	if billable := usageBillingResultBillableAmount(result); billable >= 0 && result.EffectiveRateMultiplier != nil {
		usageLog.ActualCost = billable
		usageLog.RateMultiplier = *result.EffectiveRateMultiplier
	}
	switch {
	case result.SubscriptionAmountUSD > 0:
		usageLog.BillingType = BillingTypeSubscription
	default:
		usageLog.BillingType = BillingTypeBalance
	}
}

func cloneBillingAllocations(allocations []billing.BillingAllocation) []billing.BillingAllocation {
	if len(allocations) == 0 {
		return nil
	}
	cloned := make([]billing.BillingAllocation, len(allocations))
	for i, allocation := range allocations {
		cloned[i] = billing.CloneBillingAllocation(allocation, allocation.AmountUSD)
	}
	return cloned
}

func usageBillingResultBillableAmount(result *billing.UsageBillingApplyResult) float64 {
	if result == nil {
		return -1
	}
	return result.SubscriptionAmountUSD + result.BalanceAmountUSD
}

func firstAllocatedSubscriptionID(allocations []billing.BillingAllocation) *int64 {
	for i := range allocations {
		if allocations[i].Type != billing.BillingAllocationTypeSubscription || allocations[i].SubscriptionID == nil {
			continue
		}
		subscriptionID := *allocations[i].SubscriptionID
		return &subscriptionID
	}
	return nil
}
