package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestMediaAllocationRatesPreserveBalanceMultiplier checks separate subscription and balance rates in both gateway paths; peak pricing applies to token billing.
func TestMediaAllocationRatesPreserveBalanceMultiplier(t *testing.T) {
	for _, gatewayKind := range []string{"openai", "generic"} {
		for _, mode := range []routing.BillingMode{routing.BillingModeToken, routing.BillingModeImage, routing.BillingModePerRequest, routing.BillingModeVideo} {
			if gatewayKind == "generic" && mode == routing.BillingModeVideo {
				continue
			}
			for _, peak := range []bool{false, true} {
				name := gatewayKind + "/" + string(mode) + "/" + map[bool]string{false: "normal", true: "peak"}[peak]
				t.Run(name, func(t *testing.T) {
					logs := &gatewaytestkit.UsageLogStore{Inserted: true}
					billingRepo := &gatewaytestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
					model, platform := "gpt-image-2", capability.PlatformOpenAI
					if gatewayKind == "generic" {
						model, platform = "gemini-3.1-flash-image", capability.PlatformGemini
					}
					if mode == routing.BillingModeVideo {
						model, platform = "grok-imagine-video", capability.PlatformGrok
					}
					card := routing.ModelPricingEntry{Models: []string{model}, BillingMode: mode, PerRequestPrice: testPtrFloat64(1)}
					if mode == routing.BillingModeToken {
						card.PerRequestPrice = nil
						card.InputPrice = testPtrFloat64(0.1)
					}
					group := &routing.Group{ID: 88, RateMultiplier: 2}
					settings := pricing.DefaultBillingSettings()
					settings.PeakRateEnabled, settings.PeakStart, settings.PeakEnd, settings.PeakRateMultiplier = peak, "11:00", "13:00", 3
					key := &apikey.APIKey{ID: 100, GroupID: &group.ID, Group: group}
					user, provider := &identity.User{ID: 200}, &gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 300, Platform: platform}}
					subscription := &billing.UserSubscription{ID: 99, Plan: &billing.SubscriptionPlan{ID: 199, GroupIDs: []int64{88}, GroupRateMultipliers: map[int64]float64{88: 0.5}}}
					now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.Local)
					if gatewayKind == "openai" {
						svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billingRepo, &gatewaytestkit.UserStore{}, &gatewaytestkit.SubscriptionStore{}, nil)
						svc.Dependencies.Prices = billingtestkit.SharedPriceResolver(svc.Dependencies.Calculator, group.ID, settings, []routing.ModelPricingEntry{card})
						svc.Options.Now = func() time.Time { return now }
						result := &forwardcore.OpenAIResult{RequestID: name, Model: model, ImageCount: 1, ImageSize: "1K"}
						if mode == routing.BillingModeToken {
							result.ImageCount = 0
							result.Usage = openai.ForwardUsage{InputTokens: 10}
						}
						if mode == routing.BillingModeVideo {
							result.ImageCount = 0
							result.VideoCount = 1
							result.VideoDurationSeconds = 2
							result.VideoResolution = "480p"
						}
						require.NoError(t, svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{Result: result, APIKey: key, User: user, Provider: gatewaycapture.ExecutionCompletionRecord(provider), Subscription: subscription}))
					} else {
						svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billingRepo, &gatewaytestkit.UserStore{}, &gatewaytestkit.SubscriptionStore{})
						svc.Dependencies.Prices = billingtestkit.SharedPriceResolver(svc.Dependencies.Calculator, group.ID, settings, []routing.ModelPricingEntry{card})
						svc.Options.Now = func() time.Time { return now }
						result := &forwardcore.MessagesResult{RequestID: name, Model: model, ImageCount: 1, ImageSize: "1K"}
						if mode == routing.BillingModeToken {
							result.ImageCount = 0
							result.Usage.InputTokens = 10
						}
						require.NoError(t, svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{Result: result, APIKey: key, User: user, Provider: gatewaycapture.ExecutionCompletionRecord(provider), Subscription: subscription}))
					}
					scale := 1.0
					if mode == routing.BillingModeToken && peak {
						scale = 3
					}
					require.NotNil(t, billingRepo.LastCmd)
					require.InDelta(t, 2*scale, billingRepo.LastCmd.SubscriptionRateMultiplier, 1e-12)
					require.InDelta(t, 2*scale, billingRepo.LastCmd.BalanceRateMultiplier, 1e-12)
					require.InDelta(t, scale, billingRepo.LastCmd.SubscriptionRateMultiplierScale, 1e-12)
					// The subscription receives the 0.5x rate while balance allocation retains its own rate.
					require.InDelta(t, 0.5*scale, logs.LastLog.RateMultiplier, 1e-12)
				})
			}
		}
	}
}
