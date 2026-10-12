package pricingcontract

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// contractSettlementInput contains settlement amounts, paying-user details, and rates.
type contractSettlementInput struct {
	Cost                            *pricing.CostBreakdown
	User                            *identity.User
	APIKey                          *apikey.APIKey
	Provider                        *provider.ExecutionProvider
	Subscription                    *billing.UserSubscription
	RequestPayloadHash              string
	ProviderRateMultiplier          float64
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	APIKeyService                   provider.QuotaUpdater
	Platform                        string // Platform identifier supplied by the fixture.
	// BillingBaseAmountUSD is the base amount for user fund allocation; nil uses Cost.TotalCost.
	// Free Fast uses Standard user pricing while provider statistics retain the Fast base cost.
	BillingBaseAmountUSD *float64
}

// TestBuildUsageBillingCommand_BillableAmountTracksActualCost checks that user charges use ActualCost.
func TestBuildUsageBillingCommand_BillableAmountTracksActualCost(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name         string
		totalCost    float64
		actualCost   float64
		wantBillable float64
	}{
		{
			name:         "subscription with 2x multiplier consumes 2x quota",
			totalCost:    1.0,
			actualCost:   2.0,
			wantBillable: 2.0,
		},
		{
			name:         "subscription with 0.5x multiplier consumes 0.5x quota",
			totalCost:    1.0,
			actualCost:   0.5,
			wantBillable: 0.5,
		},
		{
			name:         "free subscription (multiplier 0) consumes no quota",
			totalCost:    1.0,
			actualCost:   0,
			wantBillable: 0,
		},
		{
			name:         "balance billing keeps using ActualCost (regression)",
			totalCost:    1.0,
			actualCost:   2.0,
			wantBillable: 2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &contractSettlementInput{
				Cost:         &pricing.CostBreakdown{TotalCost: tt.totalCost, ActualCost: tt.actualCost},
				User:         &identity.User{ID: 1},
				APIKey:       &apikey.APIKey{ID: 2, GroupID: &groupID},
				Provider:     &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3}},
				Subscription: &billing.UserSubscription{ID: subID},
			}

			cmd := buildContractBillingCommand("req-1", nil, p)
			if cmd == nil {
				t.Fatal("buildContractBillingCommand returned nil")
			}
			if cmd.BillableAmountUSD != tt.wantBillable {
				t.Errorf("BillableAmountUSD = %v, want %v", cmd.BillableAmountUSD, tt.wantBillable)
			}
		})
	}
}

func TestBuildUsageBillingCommand_ProviderQuotaUsesProviderStatsCost(t *testing.T) {
	t.Parallel()

	customCost := 2.0
	zeroCost := 0.0
	tests := []struct {
		name                   string
		providerStatsCost      *float64
		totalCost              float64
		actualCost             float64
		providerRateMultiplier float64
		wantProviderQuota      float64
	}{
		{
			name:                   "自定义提供商成本乘提供商倍率",
			providerStatsCost:      &customCost,
			totalCost:              5,
			actualCost:             7,
			providerRateMultiplier: 1.5,
			wantProviderQuota:      3,
		},
		{
			name:                   "空提供商成本回退总成本",
			totalCost:              4,
			actualCost:             1.25,
			providerRateMultiplier: 2,
			wantProviderQuota:      8,
		},
		{
			name:                   "显式零提供商成本不累计额度",
			providerStatsCost:      &zeroCost,
			totalCost:              4,
			actualCost:             1.25,
			providerRateMultiplier: 3,
			wantProviderQuota:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			usageLog := &usage.UsageLog{ProviderStatsCost: tt.providerStatsCost}
			p := &contractSettlementInput{
				Cost: &pricing.CostBreakdown{
					TotalCost:  tt.totalCost,
					ActualCost: tt.actualCost,
				},
				User:                   &identity.User{ID: 1},
				APIKey:                 &apikey.APIKey{ID: 2},
				Provider:               &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3, Type: capability.ProviderTypeAPIKey, Extra: map[string]any{"quota_limit": 100}}},
				ProviderRateMultiplier: tt.providerRateMultiplier,
			}

			cmd := buildContractBillingCommand("req-provider-quota", usageLog, p)

			if cmd == nil {
				t.Fatal("buildContractBillingCommand returned nil")
			}
			if cmd.ProviderQuotaCost != tt.wantProviderQuota {
				t.Errorf("ProviderQuotaCost = %v, want %v", cmd.ProviderQuotaCost, tt.wantProviderQuota)
			}
			// Balance, subscription, and API key quota charges use ActualCost.
			if cmd.BillableAmountUSD != tt.actualCost {
				t.Errorf("BillableAmountUSD = %v, want %v", cmd.BillableAmountUSD, tt.actualCost)
			}
		})
	}
}

func TestBuildUsageBillingCommand_IncludesRequestGroupID(t *testing.T) {
	groupID := int64(42)
	p := &contractSettlementInput{
		Cost: &pricing.CostBreakdown{ActualCost: 1.25},
		User: &identity.User{ID: 10},
		APIKey: &apikey.APIKey{
			ID:      20,
			GroupID: &groupID,
		},
		Provider: &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 30, Type: capability.ProviderTypeAPIKey}},
	}

	cmd := buildContractBillingCommand("req-group", nil, p)

	if cmd == nil {
		t.Fatal("buildContractBillingCommand returned nil")
	}
	if cmd.GroupID == nil {
		t.Fatal("GroupID is nil")
	}
	if *cmd.GroupID != groupID {
		t.Fatalf("GroupID = %d, want %d", *cmd.GroupID, groupID)
	}
}

func TestBuildUsageBillingCommand_NonTokenModesKeepAllocationRates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       routing.BillingMode
		totalCost  float64
		actualCost float64
		wantRate   float64
	}{
		{name: "image rate", mode: routing.BillingModeImage, totalCost: 0.2, actualCost: 0.2, wantRate: 0.15},
		{name: "video rate", mode: routing.BillingModeVideo, totalCost: 0.08, actualCost: 0.02, wantRate: 0.15},
		{name: "per request rate", mode: routing.BillingModePerRequest, totalCost: 0.4, actualCost: 0.1, wantRate: 0.15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &contractSettlementInput{
				Cost: &pricing.CostBreakdown{
					TotalCost:   tt.totalCost,
					ActualCost:  tt.actualCost,
					BillingMode: string(tt.mode),
				},
				User:                            &identity.User{ID: 1},
				APIKey:                          &apikey.APIKey{ID: 2},
				Provider:                        &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3}},
				SubscriptionRateMultiplier:      0.15,
				SubscriptionRateMultiplierScale: 1,
				BalanceRateMultiplier:           2,
			}

			cmd := buildContractBillingCommand("req-non-token", nil, p)

			if cmd == nil {
				t.Fatal("buildContractBillingCommand returned nil")
			}
			if cmd.SubscriptionRateMultiplier != tt.wantRate {
				t.Errorf("SubscriptionRateMultiplier = %v, want %v", cmd.SubscriptionRateMultiplier, tt.wantRate)
			}
			if cmd.SubscriptionRateMultiplierScale != 1 {
				t.Errorf("SubscriptionRateMultiplierScale = %v, want 1", cmd.SubscriptionRateMultiplierScale)
			}
			if cmd.BalanceRateMultiplier != 2 {
				t.Errorf("BalanceRateMultiplier = %v, want %v", cmd.BalanceRateMultiplier, 2.0)
			}
		})
	}
}

func TestBuildUsageBillingCommand_TokenModeKeepsAllocationRates(t *testing.T) {
	t.Parallel()

	p := &contractSettlementInput{
		Cost: &pricing.CostBreakdown{
			TotalCost:   1,
			ActualCost:  0.5,
			BillingMode: string(routing.BillingModeToken),
		},
		User:                            &identity.User{ID: 1},
		APIKey:                          &apikey.APIKey{ID: 2},
		Provider:                        &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3}},
		SubscriptionRateMultiplier:      0.8,
		SubscriptionRateMultiplierScale: 1.5,
		BalanceRateMultiplier:           0.3,
	}

	cmd := buildContractBillingCommand("req-token", nil, p)

	if cmd == nil {
		t.Fatal("buildContractBillingCommand returned nil")
	}
	if cmd.SubscriptionRateMultiplier != 0.8 {
		t.Errorf("SubscriptionRateMultiplier = %v, want 0.8", cmd.SubscriptionRateMultiplier)
	}
	if cmd.SubscriptionRateMultiplierScale != 1.5 {
		t.Errorf("SubscriptionRateMultiplierScale = %v, want 1.5", cmd.SubscriptionRateMultiplierScale)
	}
	if cmd.BalanceRateMultiplier != 0.3 {
		t.Errorf("BalanceRateMultiplier = %v, want 0.3", cmd.BalanceRateMultiplier)
	}
}

// TestBuildUsageBillingCommand_UsesOverrideBaseAmountForFreeFast checks Standard user allocation alongside Fast provider quota cost.
func TestBuildUsageBillingCommand_UsesOverrideBaseAmountForFreeFast(t *testing.T) {
	standardBase := 0.4
	fastTotal := 1.2
	standardActual := 0.2
	providerStatsCost := fastTotal
	groupID := int64(88)
	providerRate := 1.5

	cmd := buildContractBillingCommand("req-free-fast-base", &usage.UsageLog{ProviderStatsCost: &providerStatsCost}, &contractSettlementInput{
		Cost: &pricing.CostBreakdown{
			TotalCost:  fastTotal,
			ActualCost: standardActual,
		},
		BillingBaseAmountUSD:   &standardBase,
		User:                   &identity.User{ID: 1},
		APIKey:                 &apikey.APIKey{ID: 2, GroupID: &groupID},
		Provider:               &provider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3, Type: capability.ProviderTypeAPIKey, Extra: map[string]any{"quota_limit": 100}}},
		ProviderRateMultiplier: providerRate,
	})

	if cmd == nil {
		t.Fatal("buildContractBillingCommand returned nil")
	}
	if cmd.BaseAmountUSD != standardBase {
		t.Fatalf("BaseAmountUSD = %v, want %v", cmd.BaseAmountUSD, standardBase)
	}
	if cmd.BillableAmountUSD != standardActual {
		t.Fatalf("BillableAmountUSD = %v, want %v", cmd.BillableAmountUSD, standardActual)
	}
	if diff := cmd.ProviderQuotaCost - fastTotal*providerRate; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("ProviderQuotaCost = %v, want %v", cmd.ProviderQuotaCost, fastTotal*providerRate)
	}
}

func buildContractBillingCommand(requestID string, usageLog *usage.UsageLog, p *contractSettlementInput) *billing.UsageBillingCommand {
	return completion.BuildCommand(requestID, querycache.Clone(usageLog), projectContractSettlement(p))
}

func projectContractSettlement(p *contractSettlementInput) *completion.SettlementInput {
	if p == nil {
		return nil
	}
	return &completion.SettlementInput{
		Cost:                            p.Cost,
		User:                            provider.ProjectCompletionPayer(p.User),
		APIKey:                          provider.ProjectCompletionKey(p.APIKey),
		Provider:                        provider.ProjectCompletionProvider(provider.ExecutionCompletionRecord(p.Provider)),
		Subscription:                    p.Subscription,
		RequestPayloadHash:              p.RequestPayloadHash,
		ProviderRateMultiplier:          p.ProviderRateMultiplier,
		SubscriptionRateMultiplier:      p.SubscriptionRateMultiplier,
		SubscriptionRateMultiplierScale: p.SubscriptionRateMultiplierScale,
		BalanceRateMultiplier:           p.BalanceRateMultiplier,
		QuotaUpdates:                    p.APIKeyService != nil,
		BillingBaseAmountUSD:            p.BillingBaseAmountUSD,
	}
}
