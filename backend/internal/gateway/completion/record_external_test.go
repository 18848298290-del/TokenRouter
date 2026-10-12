package completion_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	completiontestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
	usagecore "github.com/TokenFlux/TokenRouter/internal/usage"
)

// openAILadderCatalogJSON supplies absolute above_272k prices that the parser converts into thresholds and multipliers.
const openAILadderCatalogJSON = `{
	"gpt-5.4": {"provider": "openai", "mode": "chat", "fast_multiplier": 2, "flex_multiplier": 0.5,
		"input_cost_per_token": 2.5e-06, "output_cost_per_token": 1.5e-05,
		"cache_read_input_token_cost": 2.5e-07, "cache_creation_input_token_cost": 2.5e-06,
		"input_cost_per_token_above_272k_tokens": 5e-06,
		"output_cost_per_token_above_272k_tokens": 2.25e-05,
		"cache_read_input_token_cost_above_272k_tokens": 5e-07},
	"gpt-5.5-pro": {"provider": "openai", "mode": "chat",
		"input_cost_per_token": 3e-05, "output_cost_per_token": 1.8e-04,
		"input_cost_per_token_above_272k_tokens": 6e-05,
		"output_cost_per_token_above_272k_tokens": 2.7e-04}
}`

// modelCatalogFixture constructs an unstarted catalog for tests.
type modelCatalogFixture struct {
	pricingData map[string]*pricing.CatalogModelPricing
}

type configuredPrices struct {
	base     *billing.PriceResolver
	groupID  int64
	settings pricing.BillingSettings
	cards    []routing.ModelPricingEntry
}

func TestGatewayServiceRecordUsage_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: context.DeadlineExceeded}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordMessages(reqCtx, &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_detached_ctx",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    501,
			Quota: 100,
		},
		User:          &identity.User{ID: 601},
		Provider:      &providercore.Record{ID: 701},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	billingRepo := requireGatewayRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NoError(t, billingRepo.LastCtxErr)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, billingRepo.LastCmd.BillableAmountUSD, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
}

func TestGatewayServiceRecordUsage_BillingFingerprintIncludesRequestPayloadHash(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	payloadHash := billing.HashUsageRequestPayload([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_payload_hash",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:             &apikey.APIKey{ID: 501, Quota: 100},
		User:               &identity.User{ID: 601},
		Provider:           &providercore.Record{ID: 701},
		RequestPayloadHash: payloadHash,
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, payloadHash, billingRepo.LastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_BillingFingerprintFallsBackToContextRequestID(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	ctx := context.WithValue(context.Background(), telemetry.RequestID, "req-local-123")
	err := svc.RecordMessages(ctx, &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_payload_fallback",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 501, Quota: 100},
		User:     &identity.User{ID: 601},
		Provider: &providercore.Record{ID: 701},
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, "client:req-local-123", billingRepo.LastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_PreservesRequestedAndUpstreamModels(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	mappedModel := "claude-sonnet-4-20250514"

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "gateway_models_split",
			Usage:         upstream.TokenUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "claude-sonnet-4",
			UpstreamModel: mappedModel,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 501, Quota: 100},
		User:     &identity.User{ID: 601},
		Provider: &providercore.Record{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "claude-sonnet-4", usageRepo.LastLog.Model)
	require.Equal(t, "claude-sonnet-4", usageRepo.LastLog.RequestedModel)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, mappedModel, *usageRepo.LastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_PreservesGroupMappedUpstreamModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "gateway_channel_mapping_models",
			Usage:         upstream.TokenUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-terra",
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 501, Quota: 100},
		User:     &identity.User{ID: 601},
		Provider: &providercore.Record{ID: 701},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:    "gpt-5.6-sol",
			GroupMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.LastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.LastLog.Model)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-terra", *usageRepo.LastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_PreservesLoopedPricingConfigAndProviderUpstreamModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "gateway_looped_mapping_models",
			Usage:         upstream.TokenUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-sol",
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 501, Quota: 100},
		User:     &identity.User{ID: 601},
		Provider: &providercore.Record{ID: 701},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:    "gpt-5.6-sol",
			GroupMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.LastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.LastLog.Model)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-sol", *usageRepo.LastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_QoderUsesStandardRequestedModelPricing(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_standard_requested_model_pricing",
			Usage:         usage,
			Model:         "gpt-5.4",
			UpstreamModel: "ultimate",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    502,
			Quota: 100,
			Group: &routing.Group{RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.4", usageRepo.LastLog.Model)
	require.Equal(t, 1200, usageRepo.LastLog.InputTokens)
	require.Equal(t, 300, usageRepo.LastLog.OutputTokens)
	require.InDelta(t, expectedCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.True(t, usageRepo.LastLog.ActualCost > 0, "Qoder should reuse standard requested-model pricing instead of a Qoder-specific upstream/deferred branch")

	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
	require.Zero(t, billingRepo.LastCmd.APIKeyQuotaCost)
	require.Zero(t, billingRepo.LastCmd.APIKeyRateLimitCost)
	require.Zero(t, billingRepo.LastCmd.ProviderQuotaCost)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBasisDoesNotUseRequestedStandardPricing(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	groupID := int64(42)
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_standard_requested_model_pricing",
			Usage:         usage,
			Model:         "gpt-5.4",
			UpstreamModel: "ultimate",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "gpt-5.4",
			GroupMappedModel:   "ultimate",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedImageBasisDoesNotUseGlobalFallback(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	groupID := int64(43)
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_standard_requested_image_pricing",
			Model:         "gpt-image-1",
			UpstreamModel: "ultimate",
			ImageCount:    2,
			ImageSize:     pricing.ImageBillingSize1K,
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      503,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 603},
		Provider: &providercore.Record{ID: 703, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "gpt-image-1",
			GroupMappedModel:   "ultimate",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 2, usageRepo.LastLog.ImageCount)
	// Missing per-image pricing produces a zero-cost usage record.

	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedImageDoesNotUseGlobalFallback(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	groupID := int64(44)
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_custom_alias_image_unpriced",
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			ImageCount:    1,
			ImageSize:     pricing.ImageBillingSize1K,
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      504,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 604},
		Provider: &providercore.Record{ID: 704, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "my-qoder-image",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 1, usageRepo.LastLog.ImageCount)
	// Missing per-image pricing produces a zero-cost usage record.

	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderRequestedBasisDoesNotFallBackToGroupMappedPricing(t *testing.T) {
	groupID := int64(45)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 100, OutputTokens: 200}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_requested_source_group_mapped_manual_pricing",
			Usage:         usage,
			Model:         "ultimate",
			UpstreamModel: "ultimate",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      505,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 605},
		Provider: &providercore.Record{ID: 705, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceRequested,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderRequestedImageDoesNotUseGlobalFallback(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	groupID := int64(46)
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_requested_source_custom_image_unpriced",
			Model:         "ultimate",
			UpstreamModel: "ultimate",
			ImageCount:    1,
			ImageSize:     pricing.ImageBillingSize1K,
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      506,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 606},
		Provider: &providercore.Record{ID: 706, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "custom-image-alias",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceRequested,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 1, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
	// Missing per-image pricing produces a zero-cost usage record.

	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderAliasesInheritAvailableBuiltinPrices(t *testing.T) {
	aliases := qoder.DefaultRequestModelIDs()

	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			billingRepo := &completiontestkit.SettlementStore{}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

			usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300, CacheCreationInputTokens: 50, CacheReadInputTokens: 25}
			err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
				Result: &forwardcore.MessagesResult{
					RequestID:     "qoder_alias_" + alias,
					Usage:         usage,
					Model:         alias,
					UpstreamModel: lookupQoderAliasKeyForTest(alias),
					Duration:      time.Second,
				},
				APIKey: &apikey.APIKey{
					ID:    502,
					Quota: 100,
					Group: &routing.Group{RateMultiplier: 1},
				},
				User:     &identity.User{ID: 602},
				Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
			})

			require.NoError(t, err)
			require.Equal(t, 1, usageRepo.Calls)
			require.NotNil(t, usageRepo.LastLog)
			require.Equal(t, alias, usageRepo.LastLog.Model)
			// Priced aliases are billed; unknown routes retain unpriced records.
			if _, pricingErr := svc.Dependencies.Calculator.GetModelPricing(alias); pricingErr == nil {
				require.Positive(t, usageRepo.LastLog.TotalCost)
			} else {
				require.Zero(t, usageRepo.LastLog.TotalCost)
			}
			require.InDelta(t, usageRepo.LastLog.TotalCost*usageRepo.LastLog.RateMultiplier, usageRepo.LastLog.ActualCost, 1e-12)

			require.Equal(t, 1, billingRepo.Calls)
			require.NotNil(t, billingRepo.LastCmd)
			require.Equal(t, usageRepo.LastLog.ActualCost, billingRepo.LastCmd.BillableAmountUSD)
		})
	}
}

func TestGatewayServiceRecordUsage_QoderGroupMappedRouteKeyWithoutManualPricingUsesZeroCost(t *testing.T) {
	groupID := int64(902)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300, CacheCreationInputTokens: 50, CacheReadInputTokens: 25}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_route_key",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBasisDoesNotUseOriginalAliasPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_route_key_original_alias_pricing",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBasisUsesRouteKeyPricing(t *testing.T) {
	groupID := int64(902)
	aliasInputPrice := 0.01
	aliasOutputPrice := 0.02
	routeInputPrice := 0.50
	routeOutputPrice := 0.75
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &routeInputPrice,
		OutputPrice: &routeOutputPrice,
	}
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &aliasInputPrice,
		OutputPrice: &aliasOutputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	expectedCost := float64(usage.InputTokens)*routeInputPrice + float64(usage.OutputTokens)*routeOutputPrice
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_alias_price_over_route_price",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, expectedCost, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, expectedCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestGatewayServiceRecordUsage_QoderImplicitRequestedBasisDoesNotInferRouteKeyPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_default_alias_route_key_pricing",
			Usage:         usage,
			Model:         "qwen3.7-plus",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBlankRouteKeyDoesNotUseOriginalAliasPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		Models:      []string{"qmodel"},
		BillingMode: routing.BillingModeToken,
	}
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		Models:      []string{"qwen3.7-plus"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_blank_route_key_original_alias_pricing",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBasisIgnoresRequestedCustomPartialPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 100, OutputTokens: 100000}

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_custom_alias_partial_pricing",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "custom-qoder",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedBasisIgnoresRequestedStandardPartialPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 100, OutputTokens: 100000}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_standard_model_partial_pricing",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "gpt-5.4",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderProviderMappedCustomAliasPartialManualPricingZerosMissingFields(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 100, OutputTokens: 100000}
	expectedCost := float64(usage.InputTokens) * inputPrice

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_provider_mapped_custom_alias_partial_pricing",
			Usage:         usage,
			Model:         "custom-qoder",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "custom-qoder",
			GroupMappedModel:   "custom-qoder",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, expectedCost, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, expectedCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestGatewayServiceRecordUsage_QoderCustomMappedRouteKeyWithoutManualPricingUsesZeroCost(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300, CacheCreationInputTokens: 50, CacheReadInputTokens: 25}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_custom_alias_route_key",
			Usage:         usage,
			Model:         "custom-qoder-model",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    502,
			Quota: 100,
			Group: &routing.Group{RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "custom-qoder-model", usageRepo.LastLog.Model)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderProviderMappedImageDoesNotUseGlobalFallback(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_custom_image_alias_route_key",
			Usage:         upstream.TokenUsage{InputTokens: 10, OutputTokens: 5},
			Model:         "custom-qoder-image",
			UpstreamModel: "qmodel",
			ImageCount:    2,
			ImageSize:     "1K",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    502,
			Quota: 100,
			Group: &routing.Group{RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "custom-qoder-image",
			GroupMappedModel:   "custom-qoder-image",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
	// Missing per-image pricing produces a zero-cost usage record.

	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderUpstreamBasisDoesNotUseRequestedStandardPricing(t *testing.T) {
	groupID := int64(902)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_upstream_billing_source_route_key",
			Usage:         usage,
			Model:         "gpt-5.4-mini",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "gpt-5.4-mini",
			GroupMappedModel:   "gpt-5.4-mini",
			BillingModelSource: routing.BillingModelSourceUpstream,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderUpstreamBasisUsesStandardUpstreamPricing(t *testing.T) {
	groupID := int64(902)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4-mini", pricing.UsageTokens{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}, 1)
	require.NoError(t, err)

	err = svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_manual_only_requested_standard_upstream",
			Usage:         usage,
			Model:         "qwen3.7-plus",
			UpstreamModel: "gpt-5.4-mini",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qwen3.7-plus",
			BillingModelSource: routing.BillingModelSourceUpstream,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, expectedCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestGatewayServiceRecordUsage_QoderGroupMappedProviderStatsUsesOriginalAliasRule(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{
		ID:     groupID,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{groupID},
				Pricing: []routing.ModelPricingEntry{
					{
						Models:      []string{"qwen3.7-plus"},
						InputPrice:  &inputPrice,
						OutputPrice: &outputPrice,
					},
				},
			},
		},
	}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.GroupPolicies = pricingConfigService

	usage := upstream.TokenUsage{InputTokens: 100, OutputTokens: 50}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_group_mapped_provider_stats_alias",
			Usage:         usage,
			Model:         "qmodel",
			UpstreamModel: "qmodel",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:      "qwen3.7-plus",
			GroupMappedModel:   "qmodel",
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ProviderStatsCost)
	require.InDelta(t, 2.0, *usageRepo.LastLog.ProviderStatsCost, 1e-12)
}

func TestGatewayServiceRecordUsage_QoderBlankConfigPricingUsesZeroCost(t *testing.T) {
	groupID := int64(902)
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "auto"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300, CacheCreationInputTokens: 50, CacheReadInputTokens: 25}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_alias_blank_channel_pricing",
			Usage:         usage,
			Model:         "auto",
			UpstreamModel: "auto",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestGatewayServiceRecordUsage_QoderManualConfigPricingOverridesDefaultAliasPricing(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "auto"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(pricingConfigService, svc.Dependencies.Calculator)

	usage := upstream.TokenUsage{InputTokens: 1200, OutputTokens: 300}
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:     "qoder_alias_manual_pricing",
			Usage:         usage,
			Model:         "auto",
			UpstreamModel: "auto",
			Duration:      time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      502,
			Quota:   100,
			GroupID: &groupID,
			Group:   &routing.Group{ID: groupID, RateMultiplier: 1},
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702, Platform: capability.PlatformQoder},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, 18.0, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 18.0, usageRepo.LastLog.ActualCost, 1e-12)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, 18.0, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestGatewayServiceRecordUsage_EmptyImageSizeDefaultsBeforeBillingAndPersistence(t *testing.T) {
	imagePrice2K := 0.19
	groupID := int64(901)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:      "gateway_image_default_size",
			Model:          "gemini-image",
			ImageCount:     1,
			ImageInputSize: "auto",
			Duration:       time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      801,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1.0,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"2K": &imagePrice2K})),
		},
		User:     &identity.User{ID: 601},
		Provider: &providercore.Record{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 1, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.ImageSize)
	require.Equal(t, pricing.ImageBillingSize2K, *usageRepo.LastLog.ImageSize)
	require.NotNil(t, usageRepo.LastLog.ImageInputSize)
	require.Equal(t, "auto", *usageRepo.LastLog.ImageInputSize)
	require.NotNil(t, usageRepo.LastLog.ImageSizeSource)
	require.Equal(t, pricing.ImageSizeSourceDefault, *usageRepo.LastLog.ImageSizeSource)
	require.InDelta(t, 0.19, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.19, usageRepo.LastLog.ActualCost, 1e-12)
}

func TestGatewayServiceRecordUsage_PeakRateAffectsTokenModeImageOutputTokens(t *testing.T) {
	groupID := int64(902)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &completiontestkit.SubscriptionStore{})
	// Fix time inside the peak window; 23:59 is its exclusive end.
	svc.Options.Now = func() time.Time {
		return time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local)
	}
	svc.Dependencies.Prices = newOpenAITokenImageConfigPricingResolverForTest(t, groupID, "gemini-image")

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:  "gateway_peak_image_tokens",
			Model:      "gemini-image",
			ImageCount: 1,
			Usage: upstream.TokenUsage{
				InputTokens:       1000,
				OutputTokens:      600,
				ImageOutputTokens: 100,
			},
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      802,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1.0,
			}, pricing.BillingSettings{
				PeakRateEnabled:              true,
				PeakStart:                    "11:59",
				PeakEnd:                      "12:01",
				PeakRateMultiplier:           3.0,
				LongContextPricingEnabled:    true,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, nil),
		},
		User:     &identity.User{ID: 602},
		Provider: &providercore.Record{ID: 702},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeToken), *usageRepo.LastLog.BillingMode)
	require.Equal(t, 3.0, usageRepo.LastLog.RateMultiplier)

	textInput := 1000 * 3e-6
	textOutput := 500 * 15e-6
	imageOutput := 100 * 15e-6
	expectedActual := (textInput + textOutput + imageOutput) * 3.0

	require.InDelta(t, textInput+textOutput+imageOutput, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, imageOutput, usageRepo.LastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, expectedActual, usageRepo.LastLog.ActualCost, 1e-12)
	billingRepo := requireGatewayRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedActual, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestGatewayServiceRecordUsage_UsageLogWriteErrorDoesNotSkipBilling(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: usagecore.MarkUsageLogCreateNotPersisted(context.Canceled)}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_not_persisted",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    503,
			Quota: 100,
		},
		User:          &identity.User{ID: 603},
		Provider:      &providercore.Record{ID: 703},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	billingRepo := requireGatewayRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, billingRepo.LastCmd.BillableAmountUSD, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
}

func TestGatewayServiceRecordUsageWithLongContext_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: context.DeadlineExceeded}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordMessages(reqCtx, &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_long_context_detached_ctx",
			Usage: upstream.TokenUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    502,
			Quota: 100,
		},
		User:          &identity.User{ID: 602},
		Provider:      &providercore.Record{ID: 702},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	billingRepo := requireGatewayRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NoError(t, billingRepo.LastCtxErr)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, billingRepo.LastCmd.BillableAmountUSD, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
}

func TestGatewayServiceRecordUsage_UsesFallbackRequestIDForUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	ctx := context.WithValue(context.Background(), telemetry.RequestID, "gateway-local-fallback")
	err := svc.RecordMessages(ctx, &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 504},
		User:     &identity.User{ID: 604},
		Provider: &providercore.Record{ID: 704},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gateway-local-fallback", usageRepo.LastLog.RequestID)
}

func TestGatewayServiceRecordUsage_PrefersClientRequestIDOverUpstreamRequestID(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-stable-123")
	ctx = context.WithValue(ctx, telemetry.RequestID, "req-local-ignored")
	err := svc.RecordMessages(ctx, &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "upstream-volatile-456",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 506},
		User:     &identity.User{ID: 606},
		Provider: &providercore.Record{ID: 706},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, "client:req-local-ignored", billingRepo.LastCmd.RequestID)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "req-local-ignored", usageRepo.LastLog.RequestID)
}

func TestGatewayServiceRecordUsage_GeneratesRequestIDWhenAllSourcesMissing(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 507},
		User:     &identity.User{ID: 607},
		Provider: &providercore.Record{ID: 707},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.True(t, strings.HasPrefix(billingRepo.LastCmd.RequestID, "generated:"))
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, billingRepo.LastCmd.RequestID, usageRepo.LastLog.BillingKey)
	require.Len(t, usageRepo.LastLog.RequestID, 36)
}

func TestGatewayServiceRecordUsage_DroppedUsageLogFallsBackToSyncCreate(t *testing.T) {
	// After settlement, queue timeout triggers a synchronous log write for reconciliation (issue #3656).
	usageRepo := &completiontestkit.BestEffortUsageLogStore{
		BestEffortErr: usagecore.MarkUsageLogCreateDropped(errors.New("usage log best-effort queue full")),
	}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_drop_usage_log",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 508},
		User:     &identity.User{ID: 608},
		Provider: &providercore.Record{ID: 708},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.BestEffortCalls)
	require.Equal(t, 1, usageRepo.CreateCalls)
	// The synchronous retry receives a fresh live context.
	require.NoError(t, usageRepo.LastCtxErr)
}

func TestGatewayServiceRecordUsage_BillingErrorWritesUnsettledUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingErr := errors.New("billing tx failed")
	billingRepo := &completiontestkit.SettlementStore{Err: billingErr}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo)

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "gateway_billing_fail",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 505},
		User:     &identity.User{ID: 605},
		Provider: &providercore.Record{ID: 705},
	})

	require.ErrorIs(t, err, billingErr)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 10, usageRepo.LastLog.InputTokens)
	require.Equal(t, 6, usageRepo.LastLog.OutputTokens)
	require.Greater(t, usageRepo.LastLog.InputCost, 0.0)
	require.Greater(t, usageRepo.LastLog.OutputCost, 0.0)
	require.Greater(t, usageRepo.LastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.LastLog.ActualCost)
}

func TestGatewayServiceRecordUsage_ReasoningEffortPersisted(t *testing.T) {
	usageRepo := &completiontestkit.BestEffortUsageLogStore{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	effort := "max"
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "effort_test",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:           "claude-opus-4-6",
			Duration:        time.Second,
			ReasoningEffort: &effort,
		},
		APIKey:   &apikey.APIKey{ID: 1},
		User:     &identity.User{ID: 1},
		Provider: &providercore.Record{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ReasoningEffort)
	require.Equal(t, "max", *usageRepo.LastLog.ReasoningEffort)
}

func TestGatewayServiceRecordUsage_ReasoningEffortNil(t *testing.T) {
	usageRepo := &completiontestkit.BestEffortUsageLogStore{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})

	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID: "no_effort_test",
			Usage: upstream.TokenUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1},
		User:     &identity.User{ID: 1},
		Provider: &providercore.Record{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Nil(t, usageRepo.LastLog.ReasoningEffort)
}

func TestGatewayServiceRecordUsage_FastSpeedDowngradedByUpstreamResponse(t *testing.T) {
	usageRepo := &completiontestkit.BestEffortUsageLogStore{}
	svc, apiKey := newGatewayRecordUsageServiceWithResolverForTest(usageRepo)

	tier := "fast"
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:                   "fast_downgraded_test",
			Usage:                       upstream.TokenUsage{InputTokens: 100, OutputTokens: 50},
			Model:                       "claude-opus-5",
			Duration:                    time.Second,
			ServiceTier:                 &tier,
			UpstreamResponseServiceTier: "standard",
		},
		APIKey:   apiKey,
		User:     &identity.User{ID: 1},
		Provider: &providercore.Record{ID: 1, Platform: capability.PlatformAnthropic},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, "standard", *usageRepo.LastLog.ServiceTier)

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	standardCost, err := svc.Dependencies.Calculator.CalculateCost("claude-opus-5", tokens, 1.0)
	require.NoError(t, err)
	fastCost, err := svc.Dependencies.Calculator.CalculateCostWithServiceTier("claude-opus-5", tokens, 1.0, "fast")
	require.NoError(t, err)
	require.Greater(t, fastCost.TotalCost, standardCost.TotalCost, "fast mode must carry a premium for the test to be meaningful")
	require.InDelta(t, standardCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10)
}

func TestGatewayServiceRecordUsage_FastSpeedHonouredKeepsPremium(t *testing.T) {
	usageRepo := &completiontestkit.BestEffortUsageLogStore{}
	svc, apiKey := newGatewayRecordUsageServiceWithResolverForTest(usageRepo)

	tier := "fast"
	err := svc.RecordMessages(context.Background(), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			RequestID:                   "fast_honoured_test",
			Usage:                       upstream.TokenUsage{InputTokens: 100, OutputTokens: 50},
			Model:                       "claude-opus-5",
			Duration:                    time.Second,
			ServiceTier:                 &tier,
			UpstreamResponseServiceTier: "fast",
		},
		APIKey:   apiKey,
		User:     &identity.User{ID: 1},
		Provider: &providercore.Record{ID: 1, Platform: capability.PlatformAnthropic},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "fast", *usageRepo.LastLog.ServiceTier)

	fastCost, err := svc.Dependencies.Calculator.CalculateCostWithServiceTier("claude-opus-5", pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0, "fast")
	require.NoError(t, err)
	require.InDelta(t, fastCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10)
}

func TestOpenAIGatewayServiceRecordUsage_RejectsNilInput(t *testing.T) {
	svc := completion.NewRecorder(completion.Dependencies{}, completion.RecorderOptions{DefaultMultiplier: 1})

	require.Error(t, svc.Record(context.Background(), gatewaycapture.CaptureOpenAI(context.Background(), nil), true))
	require.Error(t, svc.Record(context.Background(), gatewaycapture.CaptureOpenAI(context.Background(), &gatewaycapture.OpenAICapture{}), true))
}

func TestOpenAIGatewayServiceRecordUsage_ZeroUsageStillWritesUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_zero_usage",
			Usage:     openai.ForwardUsage{},
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey:        &apikey.APIKey{ID: 1000, Quota: 100, Group: &routing.Group{RateMultiplier: 1}},
		User:          &identity.User{ID: 2000},
		Provider:      &providercore.Record{ID: 3000, Type: capability.ProviderTypeAPIKey},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.Equal(t, 0, userRepo.DeductCalls)
	require.Equal(t, 0, subRepo.IncrementCalls)
	require.Equal(t, 0, quotaSvc.QuotaCalls)
	require.Equal(t, 0, quotaSvc.RateLimitCalls)

	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "resp_zero_usage", usageRepo.LastLog.BillingKey)
	require.Len(t, usageRepo.LastLog.RequestID, 36)
	require.Zero(t, usageRepo.LastLog.InputTokens)
	require.Zero(t, usageRepo.LastLog.OutputTokens)
	require.Zero(t, usageRepo.LastLog.CacheCreationTokens)
	require.Zero(t, usageRepo.LastLog.CacheReadTokens)
	require.Zero(t, usageRepo.LastLog.ImageOutputTokens)
	require.Zero(t, usageRepo.LastLog.ImageCount)
	require.Zero(t, usageRepo.LastLog.InputCost)
	require.Zero(t, usageRepo.LastLog.OutputCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)

	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
	require.Zero(t, billingRepo.LastCmd.APIKeyQuotaCost)
	require.Zero(t, billingRepo.LastCmd.APIKeyRateLimitCost)
	require.Zero(t, billingRepo.LastCmd.ProviderQuotaCost)
}

func TestOpenAIGatewayServiceRecordUsage_MissingPricingRecordsZeroCostUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_missing_pricing",
			Usage: openai.ForwardUsage{
				InputTokens:  1200,
				OutputTokens: 300,
			},
			Model:    "gpt-unknown-model",
			Duration: time.Second,
		},
		APIKey:        &apikey.APIKey{ID: 1002, Quota: 100, Group: &routing.Group{RateMultiplier: 1}},
		User:          &identity.User{ID: 2002},
		Provider:      &providercore.Record{ID: 3002, Type: capability.ProviderTypeAPIKey},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.Equal(t, 0, userRepo.DeductCalls)
	require.Equal(t, 0, subRepo.IncrementCalls)
	require.Equal(t, 0, quotaSvc.QuotaCalls)
	require.Equal(t, 0, quotaSvc.RateLimitCalls)

	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "resp_missing_pricing", usageRepo.LastLog.BillingKey)
	require.Len(t, usageRepo.LastLog.RequestID, 36)
	require.Equal(t, "gpt-unknown-model", usageRepo.LastLog.Model)
	require.Equal(t, "gpt-unknown-model", usageRepo.LastLog.RequestedModel)
	require.Equal(t, 1200, usageRepo.LastLog.InputTokens)
	require.Equal(t, 300, usageRepo.LastLog.OutputTokens)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeToken), *usageRepo.LastLog.BillingMode)

	require.NotNil(t, billingRepo.LastCmd)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
	require.Zero(t, billingRepo.LastCmd.APIKeyQuotaCost)
	require.Zero(t, billingRepo.LastCmd.APIKeyRateLimitCost)
	require.Zero(t, billingRepo.LastCmd.ProviderQuotaCost)
}

func TestOpenAIGatewayServiceRecordUsage_UsesUserSpecificGroupRate(t *testing.T) {
	groupID := int64(11)
	groupRate := 1.4
	userRate := 1.8
	usage := openai.ForwardUsage{InputTokens: 15, OutputTokens: 4, CacheReadInputTokens: 3}

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	rateRepo := &completiontestkit.GroupRateStore{Rate: &userRate}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, rateRepo)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_user_group_rate",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      1001,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID:             groupID,
				RateMultiplier: groupRate,
			},
		},
		User:     &identity.User{ID: 2001},
		Provider: &providercore.Record{ID: 3001},
	})

	require.NoError(t, err)
	require.Equal(t, 1, rateRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, userRate, usageRepo.LastLog.RateMultiplier)
	require.Equal(t, 12, usageRepo.LastLog.InputTokens)
	require.Equal(t, 3, usageRepo.LastLog.CacheReadTokens)

	expected := expectedOpenAICost(t, svc, "gpt-5.4", usage, userRate)
	require.InDelta(t, expected.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expected.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_PeakRateAffectsTokenModeImageOutputTokens(t *testing.T) {
	groupID := int64(14)
	groupRate := 1.0
	usage := openai.ForwardUsage{
		InputTokens:       1000,
		OutputTokens:      600,
		ImageOutputTokens: 100,
	}

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	// Fix time inside the peak window; 23:59 is its exclusive end.
	svc.Options.Now = func() time.Time {
		return time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local)
	}
	svc.Dependencies.Prices = newOpenAITokenImageConfigPricingResolverForTest(t, groupID, "gpt-5.4")

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_peak_image_tokens",
			Usage:      usage,
			Model:      "gpt-5.4",
			Duration:   time.Second,
			ImageCount: 1,
		},
		APIKey: &apikey.APIKey{
			ID:      1004,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: groupRate,
			}, pricing.BillingSettings{
				PeakRateEnabled:              true,
				PeakStart:                    "11:59",
				PeakEnd:                      "12:01",
				PeakRateMultiplier:           3.0,
				LongContextPricingEnabled:    true,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, nil),
		},
		User:     &identity.User{ID: 2004},
		Provider: &providercore.Record{ID: 3004},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 3.0, usageRepo.LastLog.RateMultiplier)
	require.Equal(t, usage.ImageOutputTokens, usageRepo.LastLog.ImageOutputTokens)

	expected, err := svc.Dependencies.Calculator.CalculateCostUnified(billing.CostInput{
		Ctx:     context.Background(),
		Model:   "gpt-5.4",
		GroupID: i64p(groupID),
		Tokens: pricing.UsageTokens{
			InputTokens:       usage.InputTokens,
			OutputTokens:      usage.OutputTokens,
			ImageOutputTokens: usage.ImageOutputTokens,
		},
		RateMultiplier: 1.0,
		Resolver:       svc.Dependencies.Prices,
	})
	require.NoError(t, err)
	expectedActual := expected.TotalCost * 3.0

	require.InDelta(t, expected.TotalCost, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, expected.ImageOutputCost, usageRepo.LastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, expectedActual, usageRepo.LastLog.ActualCost, 1e-12)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expectedActual, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_IncludesEndpointMetadata(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	rateRepo := &completiontestkit.GroupRateStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, rateRepo)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_endpoint_metadata",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 2,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    1002,
			Group: &routing.Group{RateMultiplier: 1},
		},
		User:             &identity.User{ID: 2002},
		Provider:         &providercore.Record{ID: 3002},
		InboundEndpoint:  " /v1/chat/completions ",
		UpstreamEndpoint: " /v1/responses ",
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.InboundEndpoint)
	require.Equal(t, "/v1/chat/completions", *usageRepo.LastLog.InboundEndpoint)
	require.NotNil(t, usageRepo.LastLog.UpstreamEndpoint)
	require.Equal(t, "/v1/responses", *usageRepo.LastLog.UpstreamEndpoint)
}

func TestOpenAIGatewayServiceRecordUsage_FallsBackToGroupDefaultRateOnResolverError(t *testing.T) {
	groupID := int64(12)
	groupRate := 1.6
	usage := openai.ForwardUsage{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 2}

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	rateRepo := &completiontestkit.GroupRateStore{Err: errors.New("db unavailable")}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, rateRepo)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_group_default_on_error",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      1002,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID:             groupID,
				RateMultiplier: groupRate,
			},
		},
		User:     &identity.User{ID: 2002},
		Provider: &providercore.Record{ID: 3002},
	})

	require.NoError(t, err)
	require.Equal(t, 1, rateRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, groupRate, usageRepo.LastLog.RateMultiplier)

	expected := expectedOpenAICost(t, svc, "gpt-5.4", usage, groupRate)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expected.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_FallsBackToGroupDefaultRateWhenResolverMissing(t *testing.T) {
	groupID := int64(13)
	groupRate := 1.25
	usage := openai.ForwardUsage{InputTokens: 9, OutputTokens: 4, CacheReadInputTokens: 1}

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	svc.Dependencies.Rates = nil

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_group_default_nil_resolver",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      1003,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID:             groupID,
				RateMultiplier: groupRate,
			},
		},
		User:     &identity.User{ID: 2003},
		Provider: &providercore.Record{ID: 3003},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, groupRate, usageRepo.LastLog.RateMultiplier)
}

func TestOpenAIGatewayServiceRecordUsage_DuplicateUsageLogSkipsBilling(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: false}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_duplicate",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1004},
		User:     &identity.User{ID: 2004},
		Provider: &providercore.Record{ID: 3004},
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.Equal(t, 0, userRepo.DeductCalls)
	require.Equal(t, 0, subRepo.IncrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_DuplicateBillingKeySkipsBillingWithRepo(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: false}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_duplicate_billing_key",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    10045,
			Quota: 100,
		},
		User:          &identity.User{ID: 20045},
		Provider:      &providercore.Record{ID: 30045},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.Equal(t, 0, userRepo.DeductCalls)
	require.Equal(t, 0, subRepo.IncrementCalls)
	require.Equal(t, 0, quotaSvc.QuotaCalls)
}

func TestOpenAIGatewayServiceRecordUsage_BillsWhenUsageLogCreateReturnsError(t *testing.T) {
	usage := openai.ForwardUsage{InputTokens: 8, OutputTokens: 4}
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: errors.New("usage log batch state uncertain")}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_usage_log_error",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10041},
		User:     &identity.User{ID: 20041},
		Provider: &providercore.Record{ID: 30041},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, usageRepo.LastLog.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_UsageLogWriteErrorDoesNotSkipBilling(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: usagecore.MarkUsageLogCreateNotPersisted(context.Canceled)}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_not_persisted",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    10043,
			Quota: 100,
		},
		User:          &identity.User{ID: 20043},
		Provider:      &providercore.Record{ID: 30043},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, billingRepo.LastCmd.BillableAmountUSD, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_BillingUsesDetachedContext(t *testing.T) {
	usage := openai.ForwardUsage{InputTokens: 10, OutputTokens: 6, CacheReadInputTokens: 2}
	usageRepo := &completiontestkit.UsageLogStore{Inserted: false, Err: context.DeadlineExceeded}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordOpenAI(reqCtx, &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_detached_billing_ctx",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    10042,
			Quota: 100,
		},
		User:          &identity.User{ID: 20042},
		Provider:      &providercore.Record{ID: 30042},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NoError(t, billingRepo.LastCtxErr)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, billingRepo.LastCmd.BillableAmountUSD, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_BillingRepoUsesDetachedContext(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordOpenAI(reqCtx, &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_detached_billing_repo_ctx",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10046},
		User:     &identity.User{ID: 20046},
		Provider: &providercore.Record{ID: 30046},
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.Calls)
	require.NoError(t, billingRepo.LastCtxErr)
	require.Equal(t, 1, usageRepo.Calls)
	require.NoError(t, usageRepo.LastCtxErr)
}

func TestOpenAIGatewayServiceRecordUsage_BillingFingerprintIncludesRequestPayloadHash(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)

	payloadHash := billing.HashUsageRequestPayload([]byte(`{"model":"gpt-5","input":"hello"}`))
	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "openai_payload_hash",
			Usage: openai.ForwardUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "gpt-5",
			Duration: time.Second,
		},
		APIKey:             &apikey.APIKey{ID: 501, Quota: 100},
		User:               &identity.User{ID: 601},
		Provider:           &providercore.Record{ID: 701},
		RequestPayloadHash: payloadHash,
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, payloadHash, billingRepo.LastCmd.RequestPayloadHash)
}

func TestOpenAIGatewayServiceRecordUsage_UsesFallbackRequestIDForBillingAndUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	ctx := context.WithValue(context.Background(), telemetry.RequestID, "req-local-fallback")
	err := svc.RecordOpenAI(ctx, &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10047},
		User:     &identity.User{ID: 20047},
		Provider: &providercore.Record{ID: 30047},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, "client:req-local-fallback", billingRepo.LastCmd.RequestID)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "req-local-fallback", usageRepo.LastLog.RequestID)
}

func TestOpenAIGatewayServiceRecordUsage_PrefersClientRequestIDOverUpstreamRequestID(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "openai-client-stable-123")
	err := svc.RecordOpenAI(ctx, &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "upstream-openai-volatile-456",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10049},
		User:     &identity.User{ID: 20049},
		Provider: &providercore.Record{ID: 30049},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, "client:openai-client-stable-123", billingRepo.LastCmd.RequestID)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "openai-client-stable-123", usageRepo.LastLog.RequestID)
}

func TestOpenAIGatewayServiceRecordUsage_WSModeSeparatesTurnAndBillingIDs(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "openai-ws-turn-123")
	err := svc.RecordOpenAI(ctx, &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:    "resp_openai_ws_turn_456",
			OpenAIWSMode: true,
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10050},
		User:     &identity.User{ID: 20050},
		Provider: &providercore.Record{ID: 30050},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, "resp_openai_ws_turn_456", billingRepo.LastCmd.RequestID)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "resp_openai_ws_turn_456", usageRepo.LastLog.BillingKey)
	require.Equal(t, "openai-ws-turn-123", usageRepo.LastLog.RequestID)
}

func TestOpenAIGatewayServiceRecordUsage_GeneratesRequestIDWhenAllSourcesMissing(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10050},
		User:     &identity.User{ID: 20050},
		Provider: &providercore.Record{ID: 30050},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.LastCmd)
	require.True(t, strings.HasPrefix(billingRepo.LastCmd.RequestID, "generated:"))
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, billingRepo.LastCmd.RequestID, usageRepo.LastLog.BillingKey)
	require.Len(t, usageRepo.LastLog.RequestID, 36)
}

func TestOpenAIGatewayServiceRecordUsage_BillingErrorWritesUnsettledUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{}
	billingErr := errors.New("billing tx failed")
	billingRepo := &completiontestkit.SettlementStore{Err: billingErr}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_billing_fail",
			Usage: openai.ForwardUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10048},
		User:     &identity.User{ID: 20048},
		Provider: &providercore.Record{ID: 30048},
	})

	require.ErrorIs(t, err, billingErr)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 8, usageRepo.LastLog.InputTokens)
	require.Equal(t, 4, usageRepo.LastLog.OutputTokens)
	require.Greater(t, usageRepo.LastLog.InputCost, 0.0)
	require.Greater(t, usageRepo.LastLog.OutputCost, 0.0)
	require.Greater(t, usageRepo.LastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.LastLog.ActualCost)
}

func TestOpenAIGatewayServiceRecordUsage_UpdatesAPIKeyQuotaWhenConfigured(t *testing.T) {
	usage := openai.ForwardUsage{InputTokens: 10, OutputTokens: 6, CacheReadInputTokens: 2}
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	quotaSvc := &completiontestkit.KeyQuotaUpdater{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_quota_update",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:    1005,
			Quota: 100,
		},
		User:          &identity.User{ID: 2005},
		Provider:      &providercore.Record{ID: 3005},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	expected := expectedOpenAICost(t, svc, "gpt-5.4", usage, 1.1)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, expected.ActualCost, billingRepo.LastCmd.APIKeyQuotaCost, 1e-12)
	require.InDelta(t, 0.0, billingRepo.LastCmd.APIKeyRateLimitCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_ClampsActualInputTokensToZero(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_clamp_actual_input",
			Usage: openai.ForwardUsage{
				InputTokens:          2,
				OutputTokens:         1,
				CacheReadInputTokens: 5,
			},
			Model:    "gpt-5.4",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1006},
		User:     &identity.User{ID: 2006},
		Provider: &providercore.Record{ID: 3006},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 0, usageRepo.LastLog.InputTokens)
}

func TestOpenAIGatewayServiceRecordUsage_GPT56SeparatesCacheWriteForBillingAndStats(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	svc.Dependencies.Calculator = billingtestkit.Calculator(newModelCatalogFixture(modelCatalogFixture{pricingData: map[string]*pricing.CatalogModelPricing{
		"gpt-5.6-sol": {
			InputCostPerToken:           5e-6,
			CacheCreationInputTokenCost: 6.25e-6,
			OutputCostPerToken:          30e-6,
			CacheReadInputTokenCost:     0.5e-6,
		},
	}}), nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_gpt56_cache_write",
			Usage: openai.ForwardUsage{
				InputTokens:              1000,
				OutputTokens:             50,
				CacheCreationInputTokens: 200,
				CacheReadInputTokens:     100,
			},
			Model:    "gpt-5.6-sol",
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1056},
		User:     &identity.User{ID: 2056},
		Provider: &providercore.Record{ID: 3056},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 700, usageRepo.LastLog.InputTokens)
	require.Equal(t, 200, usageRepo.LastLog.CacheCreationTokens)
	require.Equal(t, 100, usageRepo.LastLog.CacheReadTokens)
	require.Equal(t, 1050, usageRepo.LastLog.TotalTokens())
	require.InDelta(t, 700*5e-6, usageRepo.LastLog.InputCost, 1e-12)
	require.InDelta(t, 200*6.25e-6, usageRepo.LastLog.CacheCreationCost, 1e-12)
	require.InDelta(t, 100*0.5e-6, usageRepo.LastLog.CacheReadCost, 1e-12)
	require.InDelta(t, 50*30e-6, usageRepo.LastLog.OutputCost, 1e-12)
	require.InDelta(t, usageRepo.LastLog.TotalCost*1.1, usageRepo.LastLog.ActualCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_LongContextBillingIgnoresLegacyProviderExtra(t *testing.T) {
	parentID := int64(4016)
	tests := []struct {
		name     string
		provider *providercore.Record
	}{
		{name: "missing legacy value", provider: &providercore.Record{ID: 3014, Platform: capability.PlatformOpenAI}},
		{name: "legacy false", provider: &providercore.Record{ID: 3015, Platform: capability.PlatformOpenAI, Extra: map[string]any{"openai_long_context_billing_enabled": false}}},
		{name: "legacy true", provider: &providercore.Record{ID: 3016, Platform: capability.PlatformOpenAI, Extra: map[string]any{"openai_long_context_billing_enabled": true}}},
		{name: "spark shadow", provider: &providercore.Record{
			ID:               3017,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeOAuth,
			ParentProviderID: &parentID,
			QuotaDimension:   providercore.QuotaDimensionSpark,
			Extra:            map[string]any{"openai_long_context_billing_enabled": false},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			providerRepo := &completiontestkit.ProviderLookup{Provider: &providercore.Record{ID: parentID, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
			svc := newOpenAIRecordUsageServiceForTest(
				usageRepo,
				&completiontestkit.UserStore{},
				&completiontestkit.SubscriptionStore{},
				nil,
			)
			swapInOpenAILadderCatalog(t, svc)
			svc.ProviderLookup = func(ctx context.Context, id int64) (*providercore.Record, error) {
				value,

					err := providerRepo.
					GetByID(ctx, id)
				return value, err
			}

			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID: "resp_gpt54_long_context_" + tt.name,
					Usage:     openai.ForwardUsage{InputTokens: 300000, OutputTokens: 2000},
					Model:     "gpt-5.4",
					Duration:  time.Second,
				},
				APIKey:   &apikey.APIKey{ID: 1014},
				User:     &identity.User{ID: 2014},
				Provider: tt.provider,
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.LastLog)
			expectedInput := 300000 * 2.5e-6 * 2.0
			expectedOutput := 2000 * 15e-6 * 1.5
			require.InDelta(t, expectedInput, usageRepo.LastLog.InputCost, 1e-10)
			require.InDelta(t, expectedOutput, usageRepo.LastLog.OutputCost, 1e-10)
			require.InDelta(t, expectedInput+expectedOutput, usageRepo.LastLog.TotalCost, 1e-10)
			require.InDelta(t, (expectedInput+expectedOutput)*1.1, usageRepo.LastLog.ActualCost, 1e-10)
			require.True(t, usageRepo.LastLog.LongContextBillingApplied)
			billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
			require.Equal(t, 1, billingRepo.Calls)
			require.InDelta(t, usageRepo.LastLog.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-10)
			// Shadow billing resolves parent credentials; the group setting controls long-context pricing.
			wantProviderRepoCalls := 0
			if tt.provider.IsShadow() {
				wantProviderRepoCalls = 1
			}
			require.Equal(t, wantProviderRepoCalls, providerRepo.Calls)
		})
	}
}

func TestOpenAIGatewayServiceRecordUsage_GroupControlsLongContextBilling(t *testing.T) {
	// The group setting controls the long-context policy.
	tests := []struct {
		name        string
		groupEnable bool
		wantApplied bool
	}{
		{name: "group enabled", groupEnable: true, wantApplied: true},
		{name: "group disabled", groupEnable: false, wantApplied: false},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(
				usageRepo,
				&completiontestkit.UserStore{},
				&completiontestkit.SubscriptionStore{},
				nil,
			)
			swapInOpenAILadderCatalog(t, svc)
			svc.Dependencies.Prices = billingtestkit.PriceResolver(nil, svc.Dependencies.Calculator)
			groupID := int64(1)
			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID: "resp_group_long_context_" + tt.name,
					Usage:     openai.ForwardUsage{InputTokens: 300000, OutputTokens: 2000},
					Model:     "gpt-5.4",
					Duration:  time.Second,
				},
				APIKey: &apikey.APIKey{
					ID:      int64(1020 + i),
					GroupID: &groupID,
					Group: configureBillingGroup(svc, &routing.Group{
						ID:             groupID,
						RateMultiplier: 1,
					}, pricing.BillingSettings{
						LongContextPricingEnabled:    tt.groupEnable,
						PeakRateMultiplier:           1,
						BatchImageDiscountMultiplier: 0.5,
						BatchImageHoldMultiplier:     0.6,
					}, nil),
				},
				User: &identity.User{ID: int64(2020 + i)},
				Provider: &providercore.Record{
					ID:       int64(3020 + i),
					Platform: capability.PlatformOpenAI,
					Extra:    map[string]any{"openai_long_context_billing_enabled": !tt.groupEnable},
				},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.LastLog)
			require.Equal(t, tt.wantApplied, usageRepo.LastLog.LongContextBillingApplied)
			inputMultiplier := 1.0
			outputMultiplier := 1.0
			if tt.wantApplied {
				inputMultiplier = 2.0
				outputMultiplier = 1.5
			}
			require.InDelta(t, 300000*2.5e-6*inputMultiplier, usageRepo.LastLog.InputCost, 1e-10)
			require.InDelta(t, 2000*15e-6*outputMultiplier, usageRepo.LastLog.OutputCost, 1e-10)
		})
	}
}

// TestOpenAIGatewayServiceRecordUsage_GrokLongContextFollowsGroupToggle checks group control of Grok tiered long-context prices.
func TestOpenAIGatewayServiceRecordUsage_GrokLongContextFollowsGroupToggle(t *testing.T) {
	baseInput := 250000 * 2e-6
	baseOutput := 1000 * 6e-6

	for i, tt := range []struct {
		name        string
		groupEnable bool
		wantApplied bool
	}{
		{name: "group enabled", groupEnable: true, wantApplied: true},
		{name: "group disabled", groupEnable: false, wantApplied: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(
				usageRepo,
				&completiontestkit.UserStore{},
				&completiontestkit.SubscriptionStore{},
				nil,
			)
			svc.Dependencies.Prices = billingtestkit.PriceResolver(nil, svc.Dependencies.Calculator)
			groupID := int64(10 + i)

			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID: "resp_grok_long_context_" + tt.name,
					Usage:     openai.ForwardUsage{InputTokens: 250000, OutputTokens: 1000},
					Model:     "grok-4.5",
					Duration:  time.Second,
				},
				APIKey: &apikey.APIKey{
					ID:      int64(1030 + i),
					GroupID: &groupID,
					Group: configureBillingGroup(svc, &routing.Group{
						ID:             groupID,
						RateMultiplier: 1,
					}, pricing.BillingSettings{
						LongContextPricingEnabled:    tt.groupEnable,
						PeakRateMultiplier:           1,
						BatchImageDiscountMultiplier: 0.5,
						BatchImageHoldMultiplier:     0.6,
					}, nil),
				},
				User:     &identity.User{ID: int64(2030 + i)},
				Provider: &providercore.Record{ID: int64(3030 + i), Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.LastLog)
			require.Equal(t, tt.wantApplied, usageRepo.LastLog.LongContextBillingApplied)
			multiplier := 1.0
			if tt.wantApplied {
				multiplier = 2
			}
			require.InDelta(t, baseInput*multiplier, usageRepo.LastLog.InputCost, 1e-10)
			require.InDelta(t, baseOutput*multiplier, usageRepo.LastLog.OutputCost, 1e-10)
		})
	}
}

func TestOpenAIGatewayServiceRecordUsage_ServiceTierPriorityUsesFastPricing(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	serviceTier := "priority"
	usage := openai.ForwardUsage{InputTokens: 100, OutputTokens: 50}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:   "resp_service_tier_priority",
			ServiceTier: &serviceTier,
			Usage:       usage,
			Model:       "gpt-5.4",
			Duration:    time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1015},
		User:     &identity.User{ID: 2015},
		Provider: &providercore.Record{ID: 3015},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, serviceTier, *usageRepo.LastLog.ServiceTier)

	baseCost, calcErr := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0)
	require.NoError(t, calcErr)
	require.InDelta(t, baseCost.TotalCost*2, usageRepo.LastLog.TotalCost, 1e-10)
}

func TestOpenAIGatewayServiceRecordUsage_ServiceTierFlexHalvesCost(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	serviceTier := "flex"
	usage := openai.ForwardUsage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:   "resp_service_tier_flex",
			ServiceTier: &serviceTier,
			Usage:       usage,
			Model:       "gpt-5.4",
			Duration:    time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1016},
		User:     &identity.User{ID: 2016},
		Provider: &providercore.Record{ID: 3016},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)

	baseCost, calcErr := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{InputTokens: 80, OutputTokens: 50, CacheReadTokens: 20}, 1.0)
	require.NoError(t, calcErr)
	require.InDelta(t, baseCost.TotalCost*0.5, usageRepo.LastLog.TotalCost, 1e-10)
}

func TestOpenAIGatewayServiceRecordUsage_UsesRequestedModelAndUpstreamModelMetadataFields(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	serviceTier := "priority"
	reasoning := "high"

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                "resp_billing_model_override",
			UpstreamResponseModel:    "gpt-runtime-version",
			BillingModel:             "gpt-5.1-codex",
			Model:                    "gpt-5.4",
			UpstreamModel:            "gpt-5.1-codex",
			ServiceTier:              &serviceTier,
			ReasoningEffort:          &reasoning,
			RequestedReasoningEffort: &reasoning,
			Usage: openai.ForwardUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Duration:     2 * time.Second,
			FirstTokenMs: func() *int { v := 120; return &v }(),
		},
		APIKey:    &apikey.APIKey{ID: 10, GroupID: i64p(11), Group: &routing.Group{ID: 11, RateMultiplier: 1.2}},
		User:      &identity.User{ID: 20},
		Provider:  &providercore.Record{ID: 30},
		UserAgent: "codex-cli/1.0",
		IPAddress: "127.0.0.1",
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.4", usageRepo.LastLog.Model)
	require.Equal(t, "gpt-5.4", usageRepo.LastLog.RequestedModel)
	require.Equal(t, "gpt-runtime-version", *usageRepo.LastLog.UpstreamResponseModel)
	require.True(t, *usageRepo.LastLog.UpstreamModelMismatch)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.1-codex", *usageRepo.LastLog.UpstreamModel)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, serviceTier, *usageRepo.LastLog.ServiceTier)
	require.NotNil(t, usageRepo.LastLog.ReasoningEffort)
	require.Equal(t, reasoning, *usageRepo.LastLog.ReasoningEffort)
	require.NotNil(t, usageRepo.LastLog.RequestedReasoningEffort)
	require.Equal(t, reasoning, *usageRepo.LastLog.RequestedReasoningEffort)
	require.NotNil(t, usageRepo.LastLog.UserAgent)
	require.Equal(t, "codex-cli/1.0", *usageRepo.LastLog.UserAgent)
	require.NotNil(t, usageRepo.LastLog.IPAddress)
	require.Equal(t, "127.0.0.1", *usageRepo.LastLog.IPAddress)
	require.NotNil(t, usageRepo.LastLog.GroupID)
	require.Equal(t, int64(11), *usageRepo.LastLog.GroupID)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.InDelta(t, usageRepo.LastLog.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
}

// TestOpenAIGatewayServiceRecordUsage_PersistsRequestedReasoningEffort checks separate storage of requested and forwarded effort.
func TestOpenAIGatewayServiceRecordUsage_PersistsRequestedReasoningEffort(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	requested := "max"
	forwarded := "xhigh"

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                "resp_requested_effort",
			Model:                    "gpt-5.4",
			ReasoningEffort:          &forwarded,
			RequestedReasoningEffort: &requested,
			Usage: openai.ForwardUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.RequestedReasoningEffort)
	require.Equal(t, requested, *usageRepo.LastLog.RequestedReasoningEffort)
	require.Equal(t, forwarded, *usageRepo.LastLog.ReasoningEffort)
}

func TestOpenAIGatewayServiceRecordUsage_PreservesGroupMappedUpstreamModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "openai_channel_mapping_models",
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-terra",
			Usage: openai.ForwardUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Duration: time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:    "gpt-5.6-sol",
			GroupMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.LastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.LastLog.Model)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-terra", *usageRepo.LastLog.UpstreamModel)
}

func TestOpenAIGatewayServiceRecordUsage_PreservesLoopedPricingConfigAndProviderUpstreamModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "openai_looped_mapping_models",
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-sol",
			Usage:         openai.ForwardUsage{InputTokens: 20, OutputTokens: 10},
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:    "gpt-5.6-sol",
			GroupMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.LastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.LastLog.Model)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-sol", *usageRepo.LastLog.UpstreamModel)
}

func TestOpenAIGatewayServiceRecordUsage_BillsMappedRequestsUsingRequestedModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	// Bill the requested gpt-5.4 model.
	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_upstream_model_billing_fallback",
			Model:         "gpt-5.4",
			UpstreamModel: "gpt-5.1-codex",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt-5.4", usageRepo.LastLog.Model)
	require.Equal(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost)
	require.Equal(t, expectedCost.TotalCost, usageRepo.LastLog.TotalCost)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Equal(t, 1, billingRepo.Calls)
	require.NotNil(t, billingRepo.LastCmd)
	require.Equal(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD)
}

func TestOpenAIGatewayServiceRecordUsage_GroupMappedDoesNotOverrideBillingModelWhenUnmapped(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	// Without a group remap, use the upstream billing model captured in result.BillingModel.
	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_channel_unmapped_billing",
			Model:         "glm",
			BillingModel:  "gpt-5.4",
			UpstreamModel: "gpt-5.4",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
		PricingUsageFields: routing.PricingUsageFields{
			PricingConfigID:    1,
			OriginalModel:      "glm",
			GroupMappedModel:   "glm", // No group remap.
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost)
	require.True(t, usageRepo.LastLog.ActualCost > 0, "cost must not be zero")
}

func TestOpenAIGatewayServiceRecordUsage_GroupMappedOverridesBillingModelWhenMapped(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	// A changed GroupMappedModel determines the billing model.
	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_group_mapped_billing",
			Model:         "glm",
			BillingModel:  "gpt-5.1-codex",
			UpstreamModel: "gpt-5.1-codex",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
		PricingUsageFields: routing.PricingUsageFields{
			PricingConfigID:    1,
			OriginalModel:      "glm",
			GroupMappedModel:   "gpt-5.4", // The group maps glm to gpt-5.4.
			BillingModelSource: routing.BillingModelSourceGroupMapped,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost)
	require.True(t, usageRepo.LastLog.ActualCost > 0, "cost must not be zero")
}

func TestOpenAIGatewayServiceRecordUsage_UpstreamBillingSourceOverridesRequestedModel(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	expectedCost, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4-mini", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_upstream_billing",
			Model:         "gpt-5.4",
			BillingModel:  "gpt-5.4",
			UpstreamModel: "gpt-5.4-mini",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
		PricingUsageFields: routing.PricingUsageFields{
			PricingConfigID:    1,
			OriginalModel:      "gpt-5.4",
			GroupMappedModel:   "gpt-5.4",
			BillingModelSource: routing.BillingModelSourceUpstream,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost)
	require.True(t, usageRepo.LastLog.ActualCost > 0, "cost must not be zero")
}

func TestOpenAIGatewayServiceRecordUsage_ResponsesMappedBillingModelHonorsBillingModelSource(t *testing.T) {
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}
	tokens := pricing.UsageTokens{InputTokens: 20, OutputTokens: 10}

	tests := []struct {
		name               string
		billingModelSource string
		wantBillingModel   string
	}{
		{
			name:               "upstream uses mapped billing model",
			billingModelSource: routing.BillingModelSourceUpstream,
			wantBillingModel:   "gpt-5.5",
		},
		{
			name:               "requested overrides mapped billing model",
			billingModelSource: routing.BillingModelSourceRequested,
			wantBillingModel:   "gpt-5.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			userRepo := &completiontestkit.UserStore{}
			subRepo := &completiontestkit.SubscriptionStore{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

			expectedCost, err := svc.Dependencies.Calculator.CalculateCost(tt.wantBillingModel, tokens, 1.1)
			require.NoError(t, err)

			err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID:     "resp_mapped_billing_model_source",
					Model:         "gpt-5.4",
					BillingModel:  "gpt-5.5",
					UpstreamModel: "gpt-5.5",
					Usage:         usage,
					Duration:      time.Second,
				},
				APIKey:   &apikey.APIKey{ID: 10},
				User:     &identity.User{ID: 20},
				Provider: &providercore.Record{ID: 30},
				PricingUsageFields: routing.PricingUsageFields{
					OriginalModel:      "gpt-5.4",
					GroupMappedModel:   "gpt-5.4",
					BillingModelSource: tt.billingModelSource,
				},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.LastLog)
			require.Equal(t, "gpt-5.4", usageRepo.LastLog.Model)
			require.InDelta(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
			billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
			require.NotNil(t, billingRepo.LastCmd)
			require.InDelta(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
			require.Zero(t, userRepo.DeductCalls)
			require.True(t, usageRepo.LastLog.ActualCost > 0, "cost must not be zero")
		})
	}
}

func TestOpenAIGatewayServiceRecordUsage_BillsCompactOpenAIModelAlias(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	_, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.5", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_compact_openai_alias",
			Model:         "gpt5.5",
			UpstreamModel: "gpt-5.4",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "gpt5.5", usageRepo.LastLog.Model)
	require.NotNil(t, usageRepo.LastLog.UpstreamModel)
	require.Equal(t, "gpt-5.4", *usageRepo.LastLog.UpstreamModel)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestOpenAIGatewayServiceRecordUsage_FallsBackToUpstreamModelWhenPrimaryUnpriceable(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 20, OutputTokens: 10}

	_, err := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:     "resp_unpriceable_primary_upstream_fallback",
			Model:         "not-priceable-alias",
			BillingModel:  "not-priceable-alias",
			UpstreamModel: "gpt-5.4",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.Zero(t, billingRepo.LastCmd.BillableAmountUSD)
}

func TestOpenAIGatewayServiceRecordUsage_UnpricedTokenModelFallsBackToZeroCostUsageLog(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_unpriceable_without_upstream",
			Model:     "not-priceable-alias",
			Usage:     openai.ForwardUsage{InputTokens: 20, OutputTokens: 10},
			Duration:  time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 10},
		User:     &identity.User{ID: 20},
		Provider: &providercore.Record{ID: 30},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, "not-priceable-alias", usageRepo.LastLog.Model)
	require.Equal(t, 20, usageRepo.LastLog.InputTokens)
	require.Equal(t, 10, usageRepo.LastLog.OutputTokens)
	require.Zero(t, usageRepo.LastLog.TotalCost)
	require.Zero(t, usageRepo.LastLog.ActualCost)
	require.Equal(t, 0, userRepo.DeductCalls)
	require.Equal(t, 0, subRepo.IncrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_SubscriptionBillingSetsSubscriptionFields(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	subscription := &billing.UserSubscription{ID: 99}
	planID := int64(199)
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{
		Applied:               true,
		SubscriptionAmountUSD: 12.5,
		BillingAllocations: []billing.BillingAllocation{
			{
				Type:           billing.BillingAllocationTypeSubscription,
				AmountUSD:      12.5,
				SubscriptionID: &subscription.ID,
				PlanID:         &planID,
			},
		},
	}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_subscription_billing",
			Usage:     openai.ForwardUsage{InputTokens: 10, OutputTokens: 5},
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey:       &apikey.APIKey{ID: 100, GroupID: i64p(88), Group: &routing.Group{ID: 88, RateMultiplier: 1.0}},
		User:         &identity.User{ID: 200},
		Provider:     &providercore.Record{ID: 300},
		Subscription: subscription,
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, usagecore.BillingTypeSubscription, usageRepo.LastLog.BillingType)
	require.NotNil(t, usageRepo.LastLog.SubscriptionID)
	require.Equal(t, subscription.ID, *usageRepo.LastLog.SubscriptionID)
	require.Equal(t, 1, billingRepo.Calls)
	require.Equal(t, 0, userRepo.DeductCalls)
}

func TestOpenAIGatewayServiceRecordUsage_SubscriptionBillingUsesPlanGroupRateOverUserGroupRate(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	userGroupRate := 0.17
	rateRepo := &completiontestkit.GroupRateStore{Rate: &userGroupRate}
	planID := int64(199)
	subscription := &billing.UserSubscription{
		ID: 99,
		Plan: &billing.SubscriptionPlan{
			ID:       planID,
			GroupIDs: []int64{88},
			GroupRateMultipliers: map[int64]float64{
				88: 0.5,
			},
		},
	}
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{
		Applied:               true,
		SubscriptionAmountUSD: 1,
		BillingAllocations: []billing.BillingAllocation{
			{
				Type:           billing.BillingAllocationTypeSubscription,
				AmountUSD:      1,
				SubscriptionID: &subscription.ID,
				PlanID:         &planID,
			},
		},
	}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, rateRepo)

	usage := openai.ForwardUsage{InputTokens: 10, OutputTokens: 5}
	expectedCost := expectedOpenAICost(t, svc, "gpt-5.4", usage, 0.5)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_subscription_group_rate",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      100,
			GroupID: i64p(88),
			Group: &routing.Group{
				ID:             88,
				RateMultiplier: 1.0,
			},
		},
		User:         &identity.User{ID: 200},
		Provider:     &providercore.Record{ID: 300},
		Subscription: subscription,
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, usagecore.BillingTypeSubscription, usageRepo.LastLog.BillingType)
	require.NotNil(t, usageRepo.LastLog.SubscriptionID)
	require.Equal(t, subscription.ID, *usageRepo.LastLog.SubscriptionID)
	require.InDelta(t, 0.5, usageRepo.LastLog.RateMultiplier, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
	require.Equal(t, 0, rateRepo.Calls)
}

func TestOpenAIGatewayServiceRecordUsage_InferredSubscriptionUsesPlanGroupRate(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	now := time.Now()
	subscription := billing.UserSubscription{
		ID:        99,
		UserID:    200,
		PlanID:    199,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour),
		Status:    billing.SubscriptionStatusActive,
		Plan: &billing.SubscriptionPlan{
			ID:       199,
			GroupIDs: []int64{88},
			GroupRateMultipliers: map[int64]float64{
				88: 0.5,
			},
		},
	}
	subRepo := &completiontestkit.SubscriptionStore{}
	userGroupRate := 0.17
	rateRepo := &completiontestkit.GroupRateStore{Rate: &userGroupRate}
	planID := subscription.PlanID
	billingRepo := &completiontestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{
		Applied:               true,
		SubscriptionAmountUSD: 1,
		BillingAllocations: []billing.BillingAllocation{
			{
				Type:           billing.BillingAllocationTypeSubscription,
				AmountUSD:      1,
				SubscriptionID: &subscription.ID,
				PlanID:         &planID,
			},
		},
	}}
	billingRepo.ResolveSub = &subscription
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, rateRepo)

	usage := openai.ForwardUsage{InputTokens: 10, OutputTokens: 5}
	expectedCost := expectedOpenAICost(t, svc, "gpt-5.4", usage, 0.5)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_inferred_subscription_group_rate",
			Usage:     usage,
			Model:     "gpt-5.4",
			Duration:  time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      100,
			GroupID: i64p(88),
			Group: &routing.Group{
				ID:             88,
				RateMultiplier: 1.0,
			},
		},
		User:     &identity.User{ID: 200},
		Provider: &providercore.Record{ID: 300},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, usagecore.BillingTypeSubscription, usageRepo.LastLog.BillingType)
	require.NotNil(t, usageRepo.LastLog.SubscriptionID)
	require.Equal(t, subscription.ID, *usageRepo.LastLog.SubscriptionID)
	require.InDelta(t, 0.5, usageRepo.LastLog.RateMultiplier, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, billingRepo.LastCmd.BillableAmountUSD, 1e-12)
	require.Equal(t, 0, rateRepo.Calls)
	require.Equal(t, 1, billingRepo.ResolveCalls)
}

func TestOpenAIGatewayServiceRecordUsage_ImageOnlyUsageStillPersists(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_image_only_usage",
			Model:      "gpt-image-2",
			ImageCount: 2,
			ImageSize:  "1K",
			Duration:   time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1007},
		User:     &identity.User{ID: 2007},
		Provider: &providercore.Record{ID: 3007},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 2, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.ImageSize)
	require.Equal(t, "1K", *usageRepo.LastLog.ImageSize)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
}

func TestOpenAIGatewayServiceRecordUsage_EmptyImageSizeDefaultsBeforeBillingAndPersistence(t *testing.T) {
	imagePrice2K := 0.31
	groupID := int64(1201)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_image_default_size",
			Model:      "gpt-image-2",
			ImageCount: 2,
			ImageSize:  "",
			Duration:   time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      11201,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1.0,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"2K": &imagePrice2K})),
		},
		User:     &identity.User{ID: 21201},
		Provider: &providercore.Record{ID: 31201},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 2, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.ImageSize)
	require.Equal(t, pricing.ImageBillingSize2K, *usageRepo.LastLog.ImageSize)
	require.NotNil(t, usageRepo.LastLog.ImageSizeSource)
	require.Equal(t, pricing.ImageSizeSourceDefault, *usageRepo.LastLog.ImageSizeSource)
	require.Nil(t, usageRepo.LastLog.ImageInputSize)
	require.Nil(t, usageRepo.LastLog.ImageOutputSize)
	require.InDelta(t, 0.62, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.62, usageRepo.LastLog.ActualCost, 1e-12)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
}

func TestOpenAIGatewayServiceRecordUsage_OutputImageSizeWinsBeforeBillingAndPersistence(t *testing.T) {
	imagePrice1K := 0.11
	imagePrice4K := 0.44
	groupID := int64(1202)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:        "resp_image_output_size",
			Model:            "gpt-image-2",
			ImageCount:       1,
			ImageInputSize:   "1024x1024",
			ImageOutputSizes: []string{"3840x2160"},
			Duration:         time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      11202,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1.0,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"1K": &imagePrice1K, "4K": &imagePrice4K})),
		},
		User:     &identity.User{ID: 21202},
		Provider: &providercore.Record{ID: 31202},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ImageSize)
	require.Equal(t, pricing.ImageBillingSize4K, *usageRepo.LastLog.ImageSize)
	require.NotNil(t, usageRepo.LastLog.ImageInputSize)
	require.Equal(t, "1024x1024", *usageRepo.LastLog.ImageInputSize)
	require.NotNil(t, usageRepo.LastLog.ImageOutputSize)
	require.Equal(t, "3840x2160", *usageRepo.LastLog.ImageOutputSize)
	require.NotNil(t, usageRepo.LastLog.ImageSizeSource)
	require.Equal(t, pricing.ImageSizeSourceOutput, *usageRepo.LastLog.ImageSizeSource)
	require.Equal(t, map[string]int{pricing.ImageBillingSize4K: 1}, usageRepo.LastLog.ImageSizeBreakdown)
	require.InDelta(t, 0.44, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.44, usageRepo.LastLog.ActualCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_ImageUsesPerImageBillingEvenWithUsageTokens(t *testing.T) {
	imagePrice := 0.02
	groupID := int64(12)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID: "resp_image_per_request",
			Model:     "gpt-image-2",
			Usage: openai.ForwardUsage{
				InputTokens:       1110,
				OutputTokens:      1756,
				ImageOutputTokens: 1756,
			},
			ImageCount: 2,
			ImageSize:  "1K",
			Duration:   time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      1008,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1.0,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"1K": &imagePrice})),
		},
		User:     &identity.User{ID: 2008},
		Provider: &providercore.Record{ID: 3008},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
	require.Equal(t, 2, usageRepo.LastLog.ImageCount)
	require.InDelta(t, 0.04, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.04, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, 0.0, usageRepo.LastLog.InputCost, 1e-12)
	require.InDelta(t, 0.0, usageRepo.LastLog.OutputCost, 1e-12)
	require.InDelta(t, 0.0, usageRepo.LastLog.ImageOutputCost, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_ImageSharedMultiplierPreservesExistingBehavior(t *testing.T) {
	imagePrice := 0.2
	groupID := int64(121)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_image_shared_multiplier",
			Model:      "gpt-image-2",
			ImageCount: 1,
			ImageSize:  "1K",
			Duration:   time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10121,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 0.15,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"1K": &imagePrice})),
		},
		User:     &identity.User{ID: 20121},
		Provider: &providercore.Record{ID: 30121},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, 0.2, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.03, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, 0.15, usageRepo.LastLog.RateMultiplier, 1e-12)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
}

func TestOpenAIGatewayServiceRecordUsage_ImageSharedMultiplierUsesUserGroupOverride(t *testing.T) {
	imagePrice := 0.5
	userRate := 0.2
	groupID := int64(125)

	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(
		usageRepo,
		&completiontestkit.UserStore{},
		&completiontestkit.SubscriptionStore{},
		&completiontestkit.GroupRateStore{Rate: &userRate},
	)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_image_user_group_override",
			Model:      "gpt-image-2",
			ImageCount: 1,
			ImageSize:  "1K",
			Duration:   time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10125,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 0.15,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"1K": &imagePrice})),
		},
		User:     &identity.User{ID: 20125},
		Provider: &providercore.Record{ID: 30125},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, 0.5, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.1, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, 0.2, usageRepo.LastLog.RateMultiplier, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_GrokVideoUsesDefaultRateCard(t *testing.T) {
	groupID := int64(1261)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:       "video-default-rate-card",
			ResponseID:      "video-default-rate-card",
			Model:           "grok-imagine-video-1.5",
			BillingModel:    "grok-imagine-video-1.5",
			ImageCount:      0,
			VideoCount:      1,
			VideoResolution: pricing.VideoBillingResolution720P,
			Duration:        time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      101261,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID: groupID,

				RateMultiplier: 1,
			},
		},
		User:     &identity.User{ID: 201261},
		Provider: &providercore.Record{ID: 301261, Platform: capability.PlatformGrok},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Nil(t, usageRepo.LastLog.ImageSize)
	// Missing duration uses the default eight seconds: USD 0.14/s * 8s.
	require.InDelta(t, 0.14*8, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.14*8, usageRepo.LastLog.ActualCost, 1e-12)
	require.Equal(t, 0, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeVideo), *usageRepo.LastLog.BillingMode)
	require.Equal(t, 1, usageRepo.LastLog.VideoCount)
	require.NotNil(t, usageRepo.LastLog.VideoDurationSeconds)
	require.Equal(t, pricing.VideoBillingDefaultDurationSeconds, *usageRepo.LastLog.VideoDurationSeconds)
}

func TestOpenAIGatewayServiceRecordUsage_GroupImagePriceOverridesPricingConfigImagePrice(t *testing.T) {
	groupID := int64(127)
	pricingConfigPrice := 0.201
	groupImagePrice2K := 0.021
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)
	svc.Dependencies.Prices = newOpenAIImageConfigPricingResolverForTest(t, groupID, "grok-imagine-image-quality", pricingConfigPrice)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:    "resp_grok_image_group_price",
			Model:        "grok-imagine-image-quality",
			BillingModel: "grok-imagine-image-quality",
			ImageCount:   1,
			ImageSize:    pricing.ImageBillingSize2K,
			Duration:     time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10127,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testImageModelPricing(map[string]*float64{"2K": &groupImagePrice2K})),
		},
		User:     &identity.User{ID: 20127},
		Provider: &providercore.Record{ID: 30127, Platform: capability.PlatformGrok},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 1, usageRepo.LastLog.ImageCount)
	require.Equal(t, pricing.ImageBillingSize2K, *usageRepo.LastLog.ImageSize)
	require.InDelta(t, 0.021, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.021, usageRepo.LastLog.ActualCost, 1e-12)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
}

func TestOpenAIGatewayServiceRecordUsage_GroupVideoPriceOverridesPricingConfigImagePrice(t *testing.T) {
	groupID := int64(128)
	pricingConfigPrice := 0.201
	groupVideoPrice720P := 0.037
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)
	svc.Dependencies.Prices = newOpenAIImageConfigPricingResolverForTest(t, groupID, "grok-imagine-video", pricingConfigPrice)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:            "resp_grok_video_group_price",
			Model:                "grok-imagine-video",
			BillingModel:         "grok-imagine-video",
			ImageCount:           0,
			VideoCount:           1,
			VideoResolution:      pricing.VideoBillingResolution720P,
			VideoDurationSeconds: 1,
			Duration:             time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10128,
			GroupID: i64p(groupID),
			Group: configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				RateMultiplier: 1,
			}, pricing.BillingSettings{
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, testVideoModelPricing(map[string]*float64{"720p": &groupVideoPrice720P})),
		},
		User:     &identity.User{ID: 20128},
		Provider: &providercore.Record{ID: 30128, Platform: capability.PlatformGrok},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Equal(t, 0, usageRepo.LastLog.ImageCount)
	require.Nil(t, usageRepo.LastLog.ImageSize)
	require.InDelta(t, 0.037, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.037, usageRepo.LastLog.ActualCost, 1e-12)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeVideo), *usageRepo.LastLog.BillingMode)
}

// TestOpenAIGatewayServiceRecordUsage_GrokVideoWithTokenConfigPricingKeepsVideoMetadata checks token-priced video records.
// VideoCount remains positive even when billing_mode is token and image_size is NULL.
// This keeps the video identity available to usage-log validation and reporting.
func TestOpenAIGatewayServiceRecordUsage_GrokVideoWithTokenConfigPricingKeepsVideoMetadata(t *testing.T) {
	groupID := int64(132)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)
	svc.Dependencies.Prices = newOpenAITokenImageConfigPricingResolverForTest(t, groupID, "grok-imagine-video")

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:            "resp_grok_video_token_channel",
			Model:                "grok-imagine-video",
			BillingModel:         "grok-imagine-video",
			ImageCount:           0,
			VideoCount:           1,
			VideoResolution:      pricing.VideoBillingResolution720P,
			VideoDurationSeconds: 5,
			Usage:                openai.ForwardUsage{InputTokens: 100, OutputTokens: 200},
			Duration:             time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10132,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID: groupID,

				RateMultiplier: 1,
			},
		},
		User:     &identity.User{ID: 20132},
		Provider: &providercore.Record{ID: 30132, Platform: capability.PlatformGrok},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeToken), *usageRepo.LastLog.BillingMode)
	require.Nil(t, usageRepo.LastLog.ImageSize)
	require.Equal(t, 0, usageRepo.LastLog.ImageCount)
	require.Equal(t, 1, usageRepo.LastLog.VideoCount)
	require.NotNil(t, usageRepo.LastLog.VideoResolution)
	require.Equal(t, pricing.VideoBillingResolution720P, *usageRepo.LastLog.VideoResolution)
	require.NotNil(t, usageRepo.LastLog.VideoDurationSeconds)
	require.Equal(t, 5, *usageRepo.LastLog.VideoDurationSeconds)
}

func TestOpenAIGatewayServiceRecordUsage_PricingConfigImageBillingUsesImageCountAndSharedMultiplier(t *testing.T) {
	groupID := int64(123)
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{}, nil)
	svc.Dependencies.Prices = newOpenAIImageConfigPricingResolverForTest(t, groupID, "gpt-image-2", 0.25)

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:  "resp_image_channel_shared",
			Model:      "gpt-image-2",
			ImageCount: 3,
			ImageSize:  "1K",
			Duration:   time.Second,
		},
		APIKey: &apikey.APIKey{
			ID:      10123,
			GroupID: i64p(groupID),
			Group: &routing.Group{
				ID:             groupID,
				RateMultiplier: 0.15,
			},
		},
		User:     &identity.User{ID: 20123},
		Provider: &providercore.Record{ID: 30123},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.InDelta(t, 0.75, usageRepo.LastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.1125, usageRepo.LastLog.ActualCost, 1e-12)
	require.InDelta(t, 0.15, usageRepo.LastLog.RateMultiplier, 1e-12)
	require.Equal(t, 3, usageRepo.LastLog.ImageCount)
	require.NotNil(t, usageRepo.LastLog.BillingMode)
	require.Equal(t, string(routing.BillingModeImage), *usageRepo.LastLog.BillingMode)
}

func TestOpenAIGatewayServiceRecordUsage_ServiceTierDowngradedByUpstreamResponse(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	serviceTier := "priority"
	usage := openai.ForwardUsage{InputTokens: 100, OutputTokens: 50}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                   "resp_service_tier_downgraded",
			ServiceTier:                 &serviceTier,
			UpstreamResponseServiceTier: "default",
			Usage:                       usage,
			Model:                       "gpt-5.4",
			Duration:                    time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1017},
		User:     &identity.User{ID: 2017},
		Provider: &providercore.Record{ID: 3017, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, "default", *usageRepo.LastLog.ServiceTier, "usage log must record the tier actually billed")

	baseCost, calcErr := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0)
	require.NoError(t, calcErr)
	require.InDelta(t, baseCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10, "a request served at default must not pay the priority price")
}

func TestOpenAIGatewayServiceRecordUsage_CodexDefaultEchoKeepsFastBilling(t *testing.T) {
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken} {
		t.Run(providerType, func(t *testing.T) {
			usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(
				usageRepo,
				&completiontestkit.UserStore{},
				&completiontestkit.SubscriptionStore{},
				nil,
			)
			serviceTier := "priority"
			tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID:                   "resp_codex_default_echo",
					ServiceTier:                 &serviceTier,
					UpstreamResponseServiceTier: "default",
					Usage:                       openai.ForwardUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens},
					Model:                       "gpt-5.6-sol",
					Duration:                    time.Second,
				},
				APIKey:   &apikey.APIKey{ID: 1019},
				User:     &identity.User{ID: 2019},
				Provider: &providercore.Record{ID: 3019, Platform: capability.PlatformOpenAI, Type: providerType},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.LastLog)
			require.NotNil(t, usageRepo.LastLog.ServiceTier)
			require.Equal(t, "priority", *usageRepo.LastLog.ServiceTier)

			fastCost, calcErr := svc.Dependencies.Calculator.CalculateCostWithServiceTier("gpt-5.6-sol", tokens, 1.0, "priority")
			require.NoError(t, calcErr)
			require.InDelta(t, fastCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10)
		})
	}
}

func TestOpenAIGatewayServiceRecordUsage_ShadowUsesParentCredentialTierContract(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(
		usageRepo,
		&completiontestkit.UserStore{},
		&completiontestkit.SubscriptionStore{},
		nil,
	)
	parentID := int64(9001)
	providerRepo := &completiontestkit.ProviderLookup{Provider: &providercore.Record{
		ID: parentID, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
	}}
	svc.ProviderLookup = func(ctx context.Context, id int64) (*providercore.Record, error) {
		value,

			err := providerRepo.
			GetByID(ctx, id)
		return value, err
	}

	serviceTier := "priority"
	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                   "resp_shadow_codex_default_echo",
			ServiceTier:                 &serviceTier,
			UpstreamResponseServiceTier: "default",
			Usage:                       openai.ForwardUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens},
			Model:                       "gpt-5.6-sol",
			Duration:                    time.Second,
		},
		APIKey: &apikey.APIKey{ID: 1020},
		User:   &identity.User{ID: 2020},
		Provider: &providercore.Record{
			ID: 3020, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			ParentProviderID: &parentID,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, providerRepo.Calls)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, "priority", *usageRepo.LastLog.ServiceTier)

	fastCost, calcErr := svc.Dependencies.Calculator.CalculateCostWithServiceTier("gpt-5.6-sol", tokens, 1.0, "priority")
	require.NoError(t, calcErr)
	require.InDelta(t, fastCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10)
}

// TestOpenAIGatewayServiceRecordUsage_FreeOpenAIFastChargesStandard checks standard user charges with priority base cost in logs.
func TestOpenAIGatewayServiceRecordUsage_FreeOpenAIFastChargesStandard(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(
		usageRepo,
		&completiontestkit.UserStore{},
		&completiontestkit.SubscriptionStore{},
		nil,
	)
	svc.Dependencies.Prices = billingtestkit.PriceResolver(nil, svc.Dependencies.Calculator)
	groupID := int64(77)
	serviceTier := "priority"
	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	inputPrice := 0.001
	outputPrice := 0.002
	fastMultiplier := 3.0
	apiKey := &apikey.APIKey{
		ID:      1020,
		GroupID: &groupID,
		Group: configureBillingGroup(svc, &routing.Group{
			ID:             groupID,
			Status:         billing.StatusActive,
			Hydrated:       true,
			RateMultiplier: 0.5,
		}, pricing.BillingSettings{
			FreeOpenAIFast:               true,
			LongContextPricingEnabled:    true,
			PeakRateMultiplier:           1,
			BatchImageDiscountMultiplier: 0.5,
			BatchImageHoldMultiplier:     0.6,
		}, []routing.ModelPricingEntry{{
			Models:         []string{"gpt-5.6-sol"},
			BillingMode:    routing.BillingModeToken,
			InputPrice:     &inputPrice,
			OutputPrice:    &outputPrice,
			FastMultiplier: &fastMultiplier,
		}}),
	}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                   "resp_free_fast",
			ServiceTier:                 &serviceTier,
			UpstreamResponseServiceTier: "default",
			Usage:                       openai.ForwardUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens},
			Model:                       "gpt-5.6-sol",
			Duration:                    time.Second,
		},
		APIKey:   apiKey,
		User:     &identity.User{ID: 2020},
		Provider: &providercore.Record{ID: 3020, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.NotNil(t, usageRepo.LastLog.ServiceTier)
	require.Equal(t, "priority", *usageRepo.LastLog.ServiceTier)
	require.InDelta(t, 0.5, usageRepo.LastLog.RateMultiplier, 1e-12)

	standardTotal := float64(tokens.InputTokens)*inputPrice + float64(tokens.OutputTokens)*outputPrice
	require.InDelta(t, standardTotal*fastMultiplier, usageRepo.LastLog.TotalCost, 1e-10)
	require.InDelta(t, standardTotal*0.5, usageRepo.LastLog.ActualCost, 1e-10)

	billingRepo := requireOpenAIRecordUsageBillingRepoStub(t, svc)
	require.NotNil(t, billingRepo.LastCmd)
	// Allocate subscription and balance charges from the standard base price.
	require.InDelta(t, standardTotal, billingRepo.LastCmd.BaseAmountUSD, 1e-10)
	require.InDelta(t, standardTotal*0.5, billingRepo.LastCmd.BillableAmountUSD, 1e-10)
}

func TestOpenAIGatewayServiceRecordUsage_ServiceTierNeverRaisedByUpstreamResponse(t *testing.T) {
	usageRepo := &completiontestkit.UsageLogStore{Inserted: true}
	userRepo := &completiontestkit.UserStore{}
	subRepo := &completiontestkit.SubscriptionStore{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	usage := openai.ForwardUsage{InputTokens: 100, OutputTokens: 50}

	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:                   "resp_service_tier_not_raised",
			UpstreamResponseServiceTier: "priority",
			Usage:                       usage,
			Model:                       "gpt-5.4",
			Duration:                    time.Second,
		},
		APIKey:   &apikey.APIKey{ID: 1018},
		User:     &identity.User{ID: 2018},
		Provider: &providercore.Record{ID: 3018, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	require.Nil(t, usageRepo.LastLog.ServiceTier)

	baseCost, calcErr := svc.Dependencies.Calculator.CalculateCost("gpt-5.4", pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0)
	require.NoError(t, calcErr)
	require.InDelta(t, baseCost.TotalCost, usageRepo.LastLog.TotalCost, 1e-10)
}

func requireGatewayRecordUsageBillingRepoStub(t *testing.T, svc *completiontestkit.Recording) *completiontestkit.SettlementStore {
	t.Helper()

	billingRepo, ok := svc.Dependencies.Funds.(*completiontestkit.SettlementStore)
	require.True(t, ok)
	return billingRepo
}

func lookupQoderAliasKeyForTest(alias string) string {
	if info, ok := qoder.LookupQoderModelAlias(alias); ok {
		return info.Key
	}
	return alias
}

// newGatewayRecordUsageServiceWithResolverForTest supplies a production-style price resolver for service-tier billing tests.
func newGatewayRecordUsageServiceWithResolverForTest(usageRepo usagecore.UsageLogRepository) (*completiontestkit.Recording, *apikey.APIKey) {
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &completiontestkit.UserStore{}, &completiontestkit.SubscriptionStore{})
	svc.Dependencies.Prices = billingtestkit.PriceResolver(nil, svc.Dependencies.Calculator)
	groupID := int64(7)
	return svc, &apikey.APIKey{ID: 1, GroupID: &groupID, Group: &routing.Group{ID: groupID, RateMultiplier: 1.0}}
}

// newGatewayRecordUsageServiceForTest builds the usage-recording fixture.
func newGatewayRecordUsageServiceForTest(logs usagecore.UsageLogRepository, _ identity.UserRepository, _ billing.UserSubscriptionRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, &completiontestkit.SettlementStore{}, nil, true)
}

func newGatewayRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, nil, false)
}

func requireOpenAIRecordUsageBillingRepoStub(t *testing.T, svc *completiontestkit.Recording) *completiontestkit.SettlementStore {
	t.Helper()

	billingRepo, ok := svc.Dependencies.Funds.(*completiontestkit.SettlementStore)
	require.True(t, ok)
	return billingRepo
}

func expectedOpenAICost(t *testing.T, svc *completiontestkit.Recording, model string, usage openai.ForwardUsage, multiplier float64) *pricing.CostBreakdown {
	t.Helper()

	cost, err := svc.Dependencies.Calculator.CalculateCost(model, pricing.UsageTokens{
		InputTokens:         max(usage.InputTokens-usage.CacheReadInputTokens-usage.CacheCreationInputTokens, 0),
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:     usage.CacheReadInputTokens,
	}, multiplier)
	require.NoError(t, err)
	return cost
}

// swapInOpenAILadderCatalog supplies above_272k prices for catalog-driven long-context rules.
func swapInOpenAILadderCatalog(t *testing.T, svc *completiontestkit.Recording) {
	t.Helper()
	svc.Dependencies.Calculator = NewBillingService(newStubCatalogFromJSON(t, openAILadderCatalogJSON))
}

func newOpenAITokenImageConfigPricingResolverForTest(t *testing.T, groupID int64, model string) *billing.PriceResolver {
	t.Helper()
	inputPrice := 3e-6
	outputPrice := 15e-6
	imageOutputPrice := 15e-6
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Model: model}] = &routing.ModelPricingEntry{
		BillingMode:      routing.BillingModeToken,
		InputPrice:       &inputPrice,
		OutputPrice:      &outputPrice,
		ImageOutputPrice: &imageOutputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = ""
	cache.LoadedAt = time.Now()

	cs := routingtestkit.ModelConfigFromData(cache)
	return billingtestkit.PriceResolver(cs, NewBillingService(nil))
}

// newOpenAIRecordUsageServiceForTest builds a recorder with test stores and rates.
func newOpenAIRecordUsageServiceForTest(logs usagecore.UsageLogRepository, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, &completiontestkit.SettlementStore{}, rates, true)
}

func newOpenAIRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, rates, false)
}

// testImageModelPricing supplies media prices through model price cards.
func testImageModelPricing(prices map[string]*float64) []routing.ModelPricingEntry {
	return testMediaModelPricing(routing.BillingModeImage, prices)
}

func testVideoModelPricing(prices map[string]*float64) []routing.ModelPricingEntry {
	return testMediaModelPricing(routing.BillingModeVideo, prices)
}

func testMediaModelPricing(mode routing.BillingMode, prices map[string]*float64) []routing.ModelPricingEntry {
	card := routing.ModelPricingEntry{Models: []string{"*"}, BillingMode: mode}
	for tier, price := range prices {
		card.Intervals = append(card.Intervals, routing.PricingInterval{TierLabel: tier, PerRequestPrice: price})
	}
	return []routing.ModelPricingEntry{card}
}

func newModelCatalogFixture(fixture modelCatalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// newStubCatalogFromJSON uses the production catalog parser for test pricing.
func newStubCatalogFromJSON(t *testing.T, body string) *provider.Service {
	t.Helper()
	raw, err := pricing.DecodeCatalogEntries([]byte(body))
	require.NoError(t, err)
	data, diagnostics, err := pricing.ParsePricingEntries(raw)
	require.NoError(t, diagnostics.ValidationError())
	require.NoError(t, err)
	service := newModelCatalogFixture(modelCatalogFixture{pricingData: data})
	return service
}

// configureBillingGroup installs the group settings and shared price cards in a resolver.
func configureBillingGroup(svc *completiontestkit.Recording, group *routing.Group, settings pricing.BillingSettings, cards []routing.ModelPricingEntry) *routing.Group {
	source := configuredPrices{base: svc.Dependencies.Prices, groupID: group.ID, settings: settings, cards: cards}
	svc.Dependencies.Prices = billing.NewPriceResolver(source, svc.Dependencies.Calculator, nil, nil)
	return group
}

func (s configuredPrices) GetEffectiveBillingSettings(ctx context.Context, id int64) pricing.BillingSettings {
	if id == s.groupID {
		return s.settings.Clone()
	}
	return s.base.BillingSettings(ctx, &id)
}

func (s configuredPrices) GetEffectiveConfigModelPricing(ctx context.Context, id int64, model string) *pricing.ModelPricingEntry {
	if id == s.groupID {
		if card := pricing.MatchPriceCard(s.cards, model); card != nil {
			return card
		}
	}
	if s.base != nil {
		return s.base.LookupConfigPricingNormalized(ctx, id, model)
	}
	return nil
}
