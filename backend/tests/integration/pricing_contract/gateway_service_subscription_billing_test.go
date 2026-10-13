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

// Settlement commands carry user funding amounts separately from upstream
// quota consumption. Local fixtures project payer, key, and provider inputs
// into the production command builder, then inspect billable amounts,
// allocation factors, group identity, and the Standard user base used
// alongside Fast provider cost.

// contractSettlementInput supplies records, amounts, and funding factors
// for command-builder assertions.
type contractSettlementInput struct {
	// Cost, User, APIKey, and Provider supply the builder's required
	// settlement evidence. If any is nil after projection,
	// completion.BuildCommand returns nil.
	Cost     *pricing.CostBreakdown      // calculated model and user costs
	User     *identity.User              // funding payer record
	APIKey   *apikey.APIKey              // request key record
	Provider *provider.ExecutionProvider // executing provider record
	// Subscription carries preliminary subscription evidence through
	// projection. BuildCommand does not read this field when
	// constructing these commands.
	Subscription *billing.UserSubscription
	// RequestPayloadHash is forwarded to the builder, which trims its
	// surrounding whitespace before normalization.
	RequestPayloadHash string // billing fingerprint input
	// ProviderRateMultiplier scales upstream quota cost.
	// SubscriptionRateMultiplier and BalanceRateMultiplier accompany the user
	// base amount, while SubscriptionRateMultiplierScale supplies the scale
	// for a reloaded plan rate.
	ProviderRateMultiplier          float64 // provider quota scaling factor
	SubscriptionRateMultiplier      float64 // subscription allocation rate
	SubscriptionRateMultiplierScale float64 // reloaded plan rate scale
	BalanceRateMultiplier           float64 // excess balance allocation rate
	// APIKeyService sets QuotaUpdates when its interface value is nonnil. This
	// includes typed nil implementations because the wrapper tests the
	// interface value and does not invoke its methods.
	APIKeyService provider.QuotaUpdater // key quota update capability
	Platform      string                // unused fixture platform label
	// BillingBaseAmountUSD overrides Cost.TotalCost for user funding
	// allocation, including an explicit zero amount; nil uses Cost.TotalCost.
	// Provider quota still uses the row's provider-statistics
	// amount or Cost.TotalCost.
	BillingBaseAmountUSD *float64 // user allocation base USD
}

// TestBuildUsageBillingCommand_BillableAmountTracksActualCost varies the
// calculated user amount while holding the model total fixed. It checks that
// the constructed command carries ActualCost, including a free
// charge, as BillableAmountUSD.
func TestBuildUsageBillingCommand_BillableAmountTracksActualCost(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name string // subtest selection label
		// totalCost holds the model amount while actualCost varies the user's
		// charge. wantBillable checks that the command takes the user amount
		// from actualCost, including explicit zero.
		totalCost    float64 // model base amount USD
		actualCost   float64 // calculated user amount USD
		wantBillable float64 // expected user amount USD
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

// TestBuildUsageBillingCommand_ProviderQuotaUsesProviderStatsCost supplies
// absent, zero, or positive provider-statistics amounts with separate provider
// rates. It checks upstream quota cost and the independent ActualCost user
// amount in the command.
func TestBuildUsageBillingCommand_ProviderQuotaUsesProviderStatsCost(t *testing.T) {
	t.Parallel()

	customCost := 2.0
	zeroCost := 0.0
	tests := []struct {
		name string // subtest selection label
		// providerStatsCost selects the upstream quota base when nonnil,
		// including zero; nil uses totalCost in these command-builder cases.
		providerStatsCost *float64 // configured provider cost USD
		// totalCost and actualCost represent independent model and user
		// amounts. providerRateMultiplier acts on the quota base selected by
		// providerStatsCost, and wantProviderQuota records that scaled
		// outcome.
		totalCost              float64 // model base amount USD
		actualCost             float64 // calculated user amount USD
		providerRateMultiplier float64 // provider quota scaling factor
		wantProviderQuota      float64 // expected upstream quota amount USD
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

// TestBuildUsageBillingCommand_IncludesRequestGroupID supplies a key carrying
// a positive request group identifier. It checks that the command retains a
// nonnil pointer containing that identifier.
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

// TestBuildUsageBillingCommand_NonTokenModesKeepAllocationRates supplies
// image, video, and per-request breakdowns with explicit funding factors. It
// checks that subscription rate, plan scale, and balance rate are carried
// into the command.
func TestBuildUsageBillingCommand_NonTokenModesKeepAllocationRates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string              // subtest selection label
		mode routing.BillingMode // resolved billing mode
		// totalCost and actualCost set the breakdown's calculated amounts,
		// while wantRate names the supplied subscription factor. Different
		// effective ratios check that the command keeps that factor.
		totalCost  float64 // model base amount USD
		actualCost float64 // calculated user amount USD
		wantRate   float64 // expected subscription allocation rate
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

// TestBuildUsageBillingCommand_TokenModeKeepsAllocationRates supplies a token
// breakdown whose effective user rate differs from the explicit factors. It
// checks that the command keeps the subscription, scale, and balance values.
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

// TestBuildUsageBillingCommand_UsesOverrideBaseAmountForFreeFast supplies a
// Standard funding base alongside Fast total and provider-statistics amounts.
// It checks the user allocation base, billable amount, and separately
// multiplied provider quota cost.
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
	// Dollar tolerance allows binary rounding in the
	// independent quota product.
	if diff := cmd.ProviderQuotaCost - fastTotal*providerRate; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("ProviderQuotaCost = %v, want %v", cmd.ProviderQuotaCost, fastTotal*providerRate)
	}
}

// buildContractBillingCommand clones usageLog and projects fixture inputs for
// completion.BuildCommand. Missing cost, key, payer, or provider records
// produce nil; requestID is the supplied financial operation key.
func buildContractBillingCommand(requestID string, usageLog *usage.UsageLog, p *contractSettlementInput) *billing.UsageBillingCommand {
	return completion.BuildCommand(requestID, querycache.Clone(usageLog), projectContractSettlement(p))
}

// projectContractSettlement converts fixture records into completion
// settlement metadata and returns nil for nil input. Cost, Subscription, and
// BillingBaseAmountUSD remain shared with the fixture; APIKeyService's
// interface presence becomes the captured quota-update capability.
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
