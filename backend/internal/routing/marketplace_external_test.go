package routing_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// marketplaceLoadingProtocols 是解析器给 alias 返回的客户端协议。
var marketplaceLoadingProtocols = []capability.ProtocolID{capability.ProtocolAnthropicMessages, capability.ProtocolOpenAIChatCompletions}

// modelCatalogFixture 保存测试提供的模型价格目录。
type modelCatalogFixture struct {
	pricingData map[string]*billingpricing.CatalogModelPricing
}

// marketplaceLoadingModels 为部分分组返回空目录，覆盖属性和观测查询的分组筛选。
type marketplaceLoadingModels struct{}

// marketplaceLoadingObservations 记录容量和可用率查询的分组。
type marketplaceLoadingObservations struct {
	capacityCalls      int
	capacityGroups     []int64
	availabilityGroups []int64
}

// marketplaceLoadingPrices 记录报价和兼容模态查询。
type marketplaceLoadingPrices struct {
	requests      []routing.MarketplaceQuoteRequest
	modalityCalls int
}

type marketplaceQuoteFixture struct {
	calculator *billing.Calculator
	resolver   *billing.PriceResolver
}

type marketplaceGroupRepoStub struct {
	routing.GroupRepository

	groups []routing.Group
}

type marketplaceSettingRepoStub struct {
	settingscore.Repository
	settings map[string]string
}

// TestMarketplaceBatchAttributesAndOptionalCapacity 覆盖批量读取、属性失败降级、容量开关和模型协议的透传。
func TestMarketplaceBatchAttributesAndOptionalCapacity(t *testing.T) {
	for _, attributesFail := range []bool{false, true} {
		for _, includeCapacity := range []bool{false, true} {
			groups := &marketplaceGroupRepoStub{groups: []routing.Group{
				{ID: 2, ActiveProviderCount: 1, RateMultiplier: 2},
				{ID: 1, ActiveProviderCount: 1, RateMultiplier: 1, AvailabilityProbeConfig: routing.GroupAvailabilityProbeConfig{Enabled: true}},
				{ID: 3, ActiveProviderCount: 1},
				{ID: 4, ActiveProviderCount: 1, IsExclusive: true},
				{ID: 5},
			}}
			observations := &marketplaceLoadingObservations{}
			prices := &marketplaceLoadingPrices{}
			calls := 0
			name := "display"
			modalities := []string{"image"}
			options := routing.MarketplaceOptions{
				Now:  time.Now,
				Warn: func(string, ...any) {},
				Attributes: func(_ context.Context, models map[int64][]routing.RequestableModel) (map[int64]map[string]routing.EffectiveModelAttributes, error) {
					calls++
					require.Len(t, models, 2)
					for _, id := range []int64{1, 2} {
						require.Equal(t, []routing.RequestableModel{{ID: "alias", UpstreamModels: []string{"upstream"}}}, models[id])
					}
					if attributesFail {
						return nil, errors.New("database unavailable")
					}
					return map[int64]map[string]routing.EffectiveModelAttributes{1: {"alias": {Attributes: modelcatalog.Attributes{DisplayName: &name, InputModalities: &modalities}}}}, nil
				},
			}
			svc := routing.NewMarketplace(groups, nil, marketplaceLoadingModels{}, routing.RequestableResolver{}, prices, observations, observations, options)
			result, err := svc.ListPublic(context.Background(), routing.MarketplaceListOptions{IncludeCapacity: includeCapacity})
			require.NoError(t, err)
			require.Len(t, result, 2)
			require.Equal(t, int64(2), result[0].ID)
			require.Equal(t, int64(1), result[1].ID)
			require.Equal(t, 1, calls)
			require.Zero(t, prices.modalityCalls)
			require.Len(t, prices.requests, 2)
			require.Equal(t, "priced", prices.requests[0].Model)
			require.Equal(t, float64(2), result[0].Models[0].Pricing.InputPricePerToken)
			require.Equal(t, marketplaceLoadingProtocols, result[0].Models[0].Protocols)
			require.Equal(t, marketplaceLoadingProtocols[:1], result[0].Models[0].NativeProtocols)
			require.Equal(t, []int64{1}, observations.availabilityGroups)
			require.NotNil(t, result[1].Availability)
			if includeCapacity {
				require.Equal(t, 1, observations.capacityCalls)
				require.Equal(t, []int64{2, 1}, observations.capacityGroups)
				require.NotNil(t, result[1].Capacity)
			} else {
				require.Zero(t, observations.capacityCalls)
				require.Nil(t, result[1].Capacity)
			}
			if attributesFail {
				require.Nil(t, result[1].Models[0].Attributes)
				require.Nil(t, result[1].Models[0].InputModalities)
			} else {
				require.Equal(t, name, result[1].Models[0].DisplayName)
				require.Equal(t, modalities, result[1].Models[0].InputModalities)
			}
		}
	}
}

func TestParseMarketplaceAvailabilityWindowSettings(t *testing.T) {
	tests := []struct {
		name              string
		settings          map[string]string
		wantWindowDays    int
		wantBucketMinutes int
	}{
		{
			name:              "missing settings use defaults",
			settings:          nil,
			wantWindowDays:    routing.DefaultMarketplaceAvailabilityWindowDays,
			wantBucketMinutes: routing.DefaultMarketplaceAvailabilityBucketMinutes,
		},
		{
			name: "uses stored settings",
			settings: map[string]string{
				routing.SettingKeyMarketplaceAvailabilityWindowDays:    "14",
				routing.SettingKeyMarketplaceAvailabilityBucketMinutes: "60",
			},
			wantWindowDays:    14,
			wantBucketMinutes: 60,
		},
		{
			name: "invalid settings fall back to defaults",
			settings: map[string]string{
				routing.SettingKeyMarketplaceAvailabilityWindowDays:    "-1",
				routing.SettingKeyMarketplaceAvailabilityBucketMinutes: "0",
			},
			wantWindowDays:    routing.DefaultMarketplaceAvailabilityWindowDays,
			wantBucketMinutes: routing.DefaultMarketplaceAvailabilityBucketMinutes,
		},
		{
			name: "bucket count is capped by widening bucket",
			settings: map[string]string{
				routing.SettingKeyMarketplaceAvailabilityWindowDays:    "90",
				routing.SettingKeyMarketplaceAvailabilityBucketMinutes: "5",
			},
			wantWindowDays:    90,
			wantBucketMinutes: 180,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWindowDays, gotBucketMinutes := routing.ParseMarketplaceAvailabilityWindowSettings(tt.settings)
			if gotWindowDays != tt.wantWindowDays || gotBucketMinutes != tt.wantBucketMinutes {
				t.Fatalf("parseMarketplaceAvailabilityWindowSettings() = (%d, %d), want (%d, %d)", gotWindowDays, gotBucketMinutes, tt.wantWindowDays, tt.wantBucketMinutes)
			}
		})
	}
}

func TestModelMarketplaceQoderModelUsesStandardPricing(t *testing.T) {
	svc := newMarketplaceFixture(nil, nil, newMarketplaceCalculator(nil, nil), nil)
	group := &routing.Group{ID: 1, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "claude-sonnet-4")

	if pricing.PricingMode != "token" || pricing.PriceStatus != "priced" || pricing.InputPricePerToken <= 0 || pricing.OutputPricePerToken <= 0 {
		t.Fatalf("Qoder model pricing = (%q, %q, %g, %g), want token/priced with standard prices",
			pricing.PricingMode, pricing.PriceStatus, pricing.InputPricePerToken, pricing.OutputPricePerToken)
	}
}

func TestModelMarketplaceQoderGroupMappedBasisDoesNotUseRequestedStandardPricing(t *testing.T) {
	groupID := int64(902)
	cache := routingtestkit.NewModelConfigData()
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = "qmodel"
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive, BillingModelSource: routing.BillingModelSourceGroupMapped}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.RequestableModelPricing(context.Background(), group, routing.MarketplaceModelDef{ID: "gpt-5.4", PricingModel: "qmodel"})

	if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
		t.Fatalf("Qoder channel-mapped pricing = (%q, %q, intervals=%d), want unknown/unpriced",
			pricing.PricingMode, pricing.PriceStatus, len(pricing.ContextIntervals))
	}
}

func TestModelMarketplaceQoderUpstreamBasisDoesNotUseRequestedStandardPricing(t *testing.T) {
	groupID := int64(902)
	cache := routingtestkit.NewModelConfigData()
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4-mini"}] = "qmodel"
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive, BillingModelSource: routing.BillingModelSourceUpstream}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.RequestableModelPricing(context.Background(), group, routing.MarketplaceModelDef{ID: "gpt-5.4-mini", PricingModel: "qmodel"})

	if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
		t.Fatalf("Qoder upstream route-key source pricing = (%q, %q, %g, %g), want unknown/unpriced",
			pricing.PricingMode, pricing.PriceStatus, pricing.InputPricePerToken, pricing.OutputPricePerToken)
	}
}

func TestModelMarketplaceQoderCustomImageAliasWithoutManualPricingRemainsUnknown(t *testing.T) {
	groupID := int64(902)
	cache := routingtestkit.NewModelConfigData()
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-image-alias"}] = "qmodel"
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive, BillingModelSource: routing.BillingModelSourceGroupMapped}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.RequestableModelPricing(context.Background(), group, routing.MarketplaceModelDef{ID: "custom-image-alias", PricingModel: "qmodel"})

	if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
		t.Fatalf("Qoder custom image alias pricing = (%q, %q), want unknown/unpriced", pricing.PricingMode, pricing.PriceStatus)
	}
}

func TestModelMarketplaceQoderAliasesWithoutAnyBasePricingRemainUnknown(t *testing.T) {
	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil, billingService, nil)
	group := &routing.Group{ID: 1, RateMultiplier: 1.25}

	for _, model := range []string{"auto", "qwen3.8-max", "qmodel_38max"} {
		pricing := svc.PublicModelPricing(context.Background(), group, model)
		if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
			t.Fatalf("Qoder model %s pricing = (%q, %q), want unknown/unpriced", model, pricing.PricingMode, pricing.PriceStatus)
		}
	}
}

func TestModelMarketplaceQoderManualConfigPricingOverridesDefaultAliasDisplayPricing(t *testing.T) {
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

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "auto")

	if pricing.InputPricePerToken != inputPrice || pricing.OutputPricePerToken != outputPrice {
		t.Fatalf("Qoder manual alias price = (%g, %g), want (%g, %g)", pricing.InputPricePerToken, pricing.OutputPricePerToken, inputPrice, outputPrice)
	}
}

func TestModelMarketplacePricingConfigImageInputPricingIsDisplayed(t *testing.T) {
	groupID := int64(904)
	inputPrice := 0.01
	imageInputPrice := 0.03
	outputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformOpenAI, Model: "gpt-image-edit"}] = &routing.ModelPricingEntry{
		BillingMode:     routing.BillingModeToken,
		InputPrice:      &inputPrice,
		ImageInputPrice: &imageInputPrice,
		OutputPrice:     &outputPrice,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformOpenAI
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1.5}

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-image-edit")

	if pricing.PricingMode != "token" || pricing.PriceStatus != "priced" {
		t.Fatalf("image edit pricing = (%q, %q), want token/priced", pricing.PricingMode, pricing.PriceStatus)
	}
	if pricing.ImageInputPricePerToken != imageInputPrice*group.RateMultiplier {
		t.Fatalf("image input price = %g, want %g", pricing.ImageInputPricePerToken, imageInputPrice*group.RateMultiplier)
	}
}

func TestModelDisplayPricingImageInputFastRates(t *testing.T) {
	fastModeMultiplier := 3.0
	priorityMultiplier := 2.0
	tests := []struct {
		name          string
		pricing       billingpricing.ModelPricing
		wantImage     float64
		wantFastImage float64
	}{
		{
			name: "显式 priority 倍率同步应用到图片输入价",
			pricing: billingpricing.ModelPricing{
				InputPricePerToken:      0.01,
				ImageInputPricePerToken: 0.03,
				SupportsServiceTier:     true,
				FastMultiplier:          &priorityMultiplier,
			},
			wantImage:     0.06,
			wantFastImage: 0.12,
		},
		{
			name: "独立 priority 文本价不改变显式图片输入价",
			pricing: billingpricing.ModelPricing{
				InputPricePerToken:         0.01,
				InputPricePerTokenPriority: 0.04,
				ImageInputPricePerToken:    0.03,
			},
			wantImage:     0.06,
			wantFastImage: 0.06,
		},
		{
			name: "共享价格配置 Fast 倍率同步应用到显式图片输入价",
			pricing: billingpricing.ModelPricing{
				InputPricePerToken:      0.01,
				ImageInputPricePerToken: 0.03,
				FastModeMultiplier:      &fastModeMultiplier,
			},
			wantImage:     0.06,
			wantFastImage: 0.18,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := billingpricing.BuildTokenDisplayPricing(&tt.pricing, 2)
			if pricing.ImageInputPricePerToken != tt.wantImage {
				t.Fatalf("image input price = %g, want %g", pricing.ImageInputPricePerToken, tt.wantImage)
			}
			if pricing.FastImageInputPricePerToken != tt.wantFastImage {
				t.Fatalf("fast image input price = %g, want %g", pricing.FastImageInputPricePerToken, tt.wantFastImage)
			}
		})
	}
}

func TestModelMarketplaceQoderBlankConfigPricingRemainsUnknown(t *testing.T) {
	groupID := int64(902)
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "auto"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "auto")

	if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
		t.Fatalf("Qoder blank channel alias pricing = (%q, %q), want unknown/unpriced", pricing.PricingMode, pricing.PriceStatus)
	}
}

func TestModelMarketplaceQoderBlankRouteKeyPricingShowsAliasManualPricing(t *testing.T) {
	groupID := int64(902)
	aliasInputPrice := 0.01
	aliasOutputPrice := 0.02
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
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

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "qwen3.7-plus")

	if pricing.InputPricePerToken != aliasInputPrice || pricing.OutputPricePerToken != aliasOutputPrice {
		t.Fatalf("Qoder alias display price = (%g, %g), want (%g, %g)", pricing.InputPricePerToken, pricing.OutputPricePerToken, aliasInputPrice, aliasOutputPrice)
	}
}

func TestModelMarketplaceQoderRequestedBasisDoesNotInferRouteKeyPricing(t *testing.T) {
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

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "qwen3.7-plus")

	if pricing.PricingMode != "unknown" || pricing.PriceStatus != "unpriced" {
		t.Fatalf("Qoder requested-basis display price = (%q, %q), want unknown/unpriced", pricing.PricingMode, pricing.PriceStatus)
	}
}

func TestModelMarketplaceQoderAliasManualPricingOverridesRouteKeyManualPricing(t *testing.T) {
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

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "qwen3.7-plus")

	if pricing.InputPricePerToken != aliasInputPrice || pricing.OutputPricePerToken != aliasOutputPrice {
		t.Fatalf("Qoder alias display price = (%g, %g), want (%g, %g)", pricing.InputPricePerToken, pricing.OutputPricePerToken, aliasInputPrice, aliasOutputPrice)
	}
}

func TestModelMarketplaceQoderNonUniformIntervalsDisplayAsContextIntervals(t *testing.T) {
	groupID := int64(902)
	firstInput := 0.01
	firstOutput := 0.02
	secondInput := 0.03
	secondOutput := 0.04
	maxTokens := 100
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &firstInput, OutputPrice: &firstOutput},
			{MinTokens: maxTokens, InputPrice: &secondInput, OutputPrice: &secondOutput},
		},
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "qwen3.7-plus")

	if pricing.PricingMode != "token" || pricing.PriceStatus != "priced" {
		t.Fatalf("Qoder interval display pricing = (%q, %q), want token/priced", pricing.PricingMode, pricing.PriceStatus)
	}
	if len(pricing.ContextIntervals) != 2 {
		t.Fatalf("ContextIntervals len = %d, want 2: %#v", len(pricing.ContextIntervals), pricing.ContextIntervals)
	}
	if pricing.ContextIntervals[0].InputPricePerToken != firstInput || pricing.ContextIntervals[1].InputPricePerToken != secondInput {
		t.Fatalf("interval input prices = (%g, %g), want (%g, %g)", pricing.ContextIntervals[0].InputPricePerToken, pricing.ContextIntervals[1].InputPricePerToken, firstInput, secondInput)
	}
}

func TestModelMarketplaceQoderStandardModelPartialIntervalKeepsBaseDisplayFields(t *testing.T) {
	groupID := int64(902)
	inputPrice := 0.01
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, InputPrice: &inputPrice},
		},
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	basePricing, err := billingService.GetModelPricing("gpt-5.4")
	if err != nil {
		t.Fatalf("GetModelPricing(gpt-5.4) error = %v", err)
	}
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-5.4")

	if pricing.InputPricePerToken != inputPrice || pricing.OutputPricePerToken != basePricing.OutputPricePerToken {
		t.Fatalf("Qoder standard partial interval display price = (%g, %g), want (%g, %g)",
			pricing.InputPricePerToken, pricing.OutputPricePerToken, inputPrice, basePricing.OutputPricePerToken)
	}
}

func TestModelMarketplaceUsesConfigPricingWithGroupMultiplier(t *testing.T) {
	groupID := int64(905)
	pricingConfigInput := 0.5
	pricingConfigOutput := 0.75
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformOpenAI, Model: "gpt-5.4-mini"}] = &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  &pricingConfigInput,
		OutputPrice: &pricingConfigOutput,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformOpenAI
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	billingService := newMarketplaceCalculator(nil, nil)
	svc := newMarketplaceFixture(nil, nil,

		billingService, NewModelPricingResolver(pricingConfigService, billingService),
	)
	group := &routing.Group{ID: groupID, RateMultiplier: 2}

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-5.4-mini")

	if pricing.InputPricePerToken != pricingConfigInput*group.RateMultiplier || pricing.OutputPricePerToken != pricingConfigOutput*group.RateMultiplier {
		t.Fatalf("group display price = (%g, %g), want (%g, %g)",
			pricing.InputPricePerToken, pricing.OutputPricePerToken,
			pricingConfigInput*group.RateMultiplier, pricingConfigOutput*group.RateMultiplier)
	}
}

func TestModelMarketplaceGroupExplicitZeroPricingRemainsPriced(t *testing.T) {
	zero := 0.0
	billingService := newMarketplaceCalculator(nil, nil)
	settings := billingpricing.DefaultBillingSettings()
	svc := marketplaceWithConfig(billingService, 906, settings, []routing.ModelPricingEntry{{Models: []string{"gpt-5.4"}, BillingMode: routing.BillingModeToken, InputPrice: &zero, OutputPrice: &zero}})
	group := &routing.Group{ID: 906, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-5.4")

	if pricing.PricingMode != "token" || pricing.PriceStatus != "priced" || pricing.InputPricePerToken != 0 || pricing.OutputPricePerToken != 0 {
		t.Fatalf("free group display pricing = %#v, want token/priced with zero prices", pricing)
	}
}

func TestModelMarketplaceGroupCanDisableBuiltInLongContextDisplay(t *testing.T) {
	billingService := newMarketplaceCalculator(nil, nil)
	settings := billingpricing.DefaultBillingSettings()
	settings.LongContextPricingEnabled = false
	svc := marketplaceWithConfig(billingService, 907, settings, nil)
	group := &routing.Group{ID: 907, RateMultiplier: 1}

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-5.4")

	if pricing.PricingMode != "token" || pricing.PriceStatus != "priced" || len(pricing.ContextIntervals) != 0 {
		t.Fatalf("long-context-disabled display pricing = %#v, want flat token pricing", pricing)
	}
}

func TestModelMarketplaceDoesNotInventModelsWithoutCandidates(t *testing.T) {
	settingRepo := &marketplaceSettingRepoStub{settings: map[string]string{
		billing.SettingKeyReasoningPointRMBUnitPrice: "1",
		billing.SettingKeyUSDExchangeRate:            "7",
	}}
	svc := newMarketplaceFixture(
		&marketplaceGroupRepoStub{groups: []routing.Group{{
			ID:                  1,
			Name:                "Qoder",
			Status:              billing.StatusActive,
			RateMultiplier:      1,
			ActiveProviderCount: 1,
		}}},
		settingRepo, newMarketplaceCalculator(nil, nil), nil,
	)

	groups, err := svc.ListPublic(context.Background(), routing.MarketplaceListOptions{IncludeCapacity: true})
	if err != nil {
		t.Fatalf("ListPublic returned error: %v", err)
	}
	require.Empty(t, groups, "提供商计数不能替代实际可请求能力")
}

func TestModelMarketplaceDisplayPricing_SharedImageRateUsesGroupMultiplier(t *testing.T) {
	image1K := 10.0
	group := &routing.Group{
		ID:             1,
		RateMultiplier: 2.0,
	}
	svc := marketplaceWithConfig(newMarketplaceCalculator(nil, map[string]*billingpricing.ModelPricing{}), 1, billingpricing.DefaultBillingSettings(), testImageModelPricing(map[string]*float64{"1K": &image1K}))

	pricing := svc.PublicModelPricing(context.Background(), group, "gpt-image-1")

	if pricing.PricingMode != "image" {
		t.Fatalf("pricing mode = %q, want image", pricing.PricingMode)
	}
	if pricing.ImagePrice1K != 20 {
		t.Fatalf("image 1K price = %v, want 20", pricing.ImagePrice1K)
	}
}

func TestModelMarketplaceModelModalitiesComeFromPricingMetadata(t *testing.T) {
	pricingSvc := newModelCatalogFixture(modelCatalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{
		"gpt-image-2": {Mode: "image_generation", InputCostPerImageToken: 8e-6},
		"gpt-5.5":     {Mode: "chat", SupportsVision: true},
	}})
	billingService := newMarketplaceCalculator(pricingSvc, nil)
	svc := newMarketplaceFixture(nil, nil, billingService, nil)

	input, output := svc.ModelModalities(routing.MarketplaceModelDef{ID: "gpt-image-2"})
	require.Equal(t, []string{"text", "image"}, input)
	require.Equal(t, []string{"image"}, output)

	input, output = svc.ModelModalities(routing.MarketplaceModelDef{ID: "gpt-5.5"})
	require.Equal(t, []string{"text", "image"}, input)
	require.Equal(t, []string{"text"}, output)

	input, output = svc.ModelModalities(routing.MarketplaceModelDef{ID: "totally-unknown-model"})
	require.Nil(t, input)
	require.Nil(t, output)
}

func TestModelMarketplacePublicModelsIncludeModalities(t *testing.T) {
	pricingSvc := newModelCatalogFixture(modelCatalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{
		"gpt-image-2": {Mode: "image_generation", InputCostPerImageToken: 8e-6},
	}})
	billingService := newMarketplaceCalculator(pricingSvc, nil)
	svc := newMarketplaceFixture(nil, nil, billingService, nil)
	group := &routing.Group{ID: 1, RateMultiplier: 1}

	models := svc.BuildPublicModels(context.Background(), group, []routing.MarketplaceModelDef{
		{ID: "gpt-image-2", DisplayName: "GPT Image 2"},
		{ID: "custom-unknown", DisplayName: "Custom Unknown"},
	})

	require.Len(t, models, 2)
	require.Equal(t, []string{"text", "image"}, models[0].InputModalities)
	require.Equal(t, []string{"image"}, models[0].OutputModalities)
	require.Nil(t, models[1].InputModalities)
	require.Nil(t, models[1].OutputModalities)
}

// TestModelMarketplaceGeminiTierModalitiesPreservePublicIDs 验证市场使用解析后的 PricingModel 查询能力，保留公开 ID 和完整音视频输入标记。
func TestModelMarketplaceGeminiTierModalitiesPreservePublicIDs(t *testing.T) {
	pricing := &billingpricing.CatalogModelPricing{
		InputCostPerToken: 2e-6, OutputCostPerToken: 1e-5,
		CacheCreationInputTokenCost: 2.5e-6, CacheReadInputTokenCost: 2e-7,
		LongContextInputTokenThreshold: 200000, LongContextInputCostMultiplier: 2,
		LongContextOutputCostMultiplier: 1.5, Mode: "chat",
		SupportedModalities:       []string{"text", "image", "audio", "video"},
		SupportedOutputModalities: []string{"text"},
	}
	pricingSvc := newModelCatalogFixture(modelCatalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{
		"gemini-3.7-flash-tiered": pricing, "gemini-3.8-flash-tiered": pricing,
	}})
	svc := newMarketplaceFixture(nil, nil, newMarketplaceCalculator(pricingSvc, nil), nil)
	defs := []routing.MarketplaceModelDef{
		{ID: "gemini-3.7-flash-tiered"},
		{ID: "gemini-3.8-flash-tiered"},
		{ID: "public-google", PricingModel: "gemini-3.8-flash-tiered"},
	}
	models := svc.BuildPublicModels(context.Background(), &routing.Group{ID: 1, RateMultiplier: 1}, defs)
	require.Len(t, models, len(defs))
	for i, model := range models {
		require.Equal(t, defs[i].ID, model.ID)
		require.Equal(t, []string{"text", "image", "audio", "video"}, model.InputModalities)
		require.Equal(t, []string{"text"}, model.OutputModalities)
	}
}

func newModelCatalogFixture(fixture modelCatalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// newMarketplaceFixture 为市场测试绑定价格配置和模型目录。
func newMarketplaceFixture(groups routing.MarketplaceGroups, settings routing.MarketplaceSettings, calculator *billing.Calculator, resolver *billing.PriceResolver) *routing.Marketplace {
	var prices routing.MarketplacePrices
	if calculator != nil {
		prices = marketplaceQuoteFixture{calculator: calculator, resolver: resolver}
	}
	return routing.NewMarketplace(groups, settings, nil, routing.RequestableResolver{}, prices, nil, nil, routing.MarketplaceOptions{Now: time.Now, Warn: slog.Warn})
}

func newMarketplaceCalculator(catalog *provider.Service, prices map[string]*billingpricing.ModelPricing) *billing.Calculator {
	return billingtestkit.Calculator(catalog, prices)
}

func NewModelPricingResolver(pricingConfigs *routing.PricingConfigService, calculator *billing.Calculator) *billing.PriceResolver {
	var source billing.ConfigPrices
	if pricingConfigs != nil {
		source = pricingConfigs
	}
	return billing.NewPriceResolver(source, calculator, modelidentity.Identity, func(model string, err error) {
		slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
	})
}

func (marketplaceLoadingModels) Prefetch(context.Context) ([]routing.CatalogueProvider, bool, error) {
	return nil, false, nil
}

func (marketplaceLoadingModels) ResolveRequestableModels(_ context.Context, id *int64, _ string) routing.RequestableModelsResult {
	if *id > 2 {
		return routing.RequestableModelsResult{}
	}
	return routing.RequestableModelsResult{Models: []routing.RequestableModel{{ID: "alias", PricingModel: "priced", UpstreamModels: []string{"upstream"}, Protocols: marketplaceLoadingProtocols, NativeProtocols: marketplaceLoadingProtocols[:1]}}}
}

func (o *marketplaceLoadingObservations) GetGroupCapacityByIDs(_ context.Context, ids []int64) (map[int64]routing.GroupCapacitySummary, error) {
	o.capacityCalls++
	o.capacityGroups = ids
	return map[int64]routing.GroupCapacitySummary{1: {}}, nil
}

func (o *marketplaceLoadingObservations) GetSummaryByGroupIDs(_ context.Context, ids []int64, _ int, _ int, _ string, _ time.Time) (map[int64]*routing.GroupAvailabilitySummary, error) {
	o.availabilityGroups = ids
	return map[int64]*routing.GroupAvailabilitySummary{1: {WindowDays: 3}}, nil
}

func (p *marketplaceLoadingPrices) Quote(_ context.Context, request routing.MarketplaceQuoteRequest) billingpricing.ModelDisplayPricing {
	p.requests = append(p.requests, request)
	return billingpricing.ModelDisplayPricing{PriceStatus: "priced", InputPricePerToken: request.RateMultiplier}
}

func (p *marketplaceLoadingPrices) GetModelModalities(string) ([]string, []string) {
	p.modalityCalls++
	return []string{"text"}, []string{"text"}
}

// testImageModelPricing 创建图片模型的价格配置。
func testImageModelPricing(prices map[string]*float64) []routing.ModelPricingEntry {
	return testMediaModelPricing(routing.BillingModeImage, prices)
}

func testMediaModelPricing(mode routing.BillingMode, prices map[string]*float64) []routing.ModelPricingEntry {
	card := routing.ModelPricingEntry{Models: []string{"*"}, BillingMode: mode}
	for tier, price := range prices {
		card.Intervals = append(card.Intervals, routing.PricingInterval{TierLabel: tier, PerRequestPrice: price})
	}
	return []routing.ModelPricingEntry{card}
}

func (p marketplaceQuoteFixture) Quote(ctx context.Context, req routing.MarketplaceQuoteRequest) billingpricing.ModelDisplayPricing {
	resolver := p.resolver
	if resolver == nil {
		resolver = billing.NewPriceResolver(nil, p.calculator, modelidentity.Identity, func(model string, err error) {
			slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
		})
	}
	return resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{Model: req.Model, GroupID: &req.GroupID}, RateMultiplier: req.RateMultiplier, FreeFastApplicable: req.FreeFastApplicable})
}

func (p marketplaceQuoteFixture) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

// marketplaceWithConfig 将市场报价连接到共享价格配置解析器。
func marketplaceWithConfig(calculator *billing.Calculator, groupID int64, settings billingpricing.BillingSettings, cards []routing.ModelPricingEntry) *routing.Marketplace {
	configs := routingtestkit.ModelConfigFromData(routingtestkit.ModelConfigDataFromRows([]routingtestkit.Configuration{{ID: 1, Status: routing.StatusActive, GroupIDs: []int64{groupID}, BillingSettings: &settings, ModelPricing: cards}}, nil))
	return newMarketplaceFixture(nil, nil, calculator, NewModelPricingResolver(configs, calculator))
}

func (s *marketplaceGroupRepoStub) ListActive(context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

func (s *marketplaceSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.settings[key]
	}
	return out, nil
}

func TestMarketplaceUsesRouteAttributes(t *testing.T) {
	attrs := &routing.ModelAttributeService{Catalog: routing.ModelAttributeCatalog{View: testkit.OfflineView}}
	service := routing.NewMarketplace(nil, nil, nil, routing.RequestableResolver{}, nil, nil, nil, routing.MarketplaceOptions{Attributes: attrs.ResolveGroups})
	target := modelcatalog.Target{Model: "gpt-5.3-codex-spark", Platform: "openai", ProviderType: "oauth", ClientProtocol: "openai_responses", UpstreamProtocol: "openai_responses"}
	result := service.BuildPublicModels(context.Background(), &routing.Group{ID: 1}, []routing.MarketplaceModelDef{{ID: "alias", UpstreamModels: []string{target.Model}, AttributeTargets: []modelcatalog.Target{target}}})
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Attributes)
	require.Equal(t, []string{"text"}, *result[0].Attributes.InputModalities)
	require.False(t, *result[0].Attributes.Temperature)
}
