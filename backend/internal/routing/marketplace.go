package routing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	SettingKeyMarketplaceAvailabilityWindowDays    = "marketplace_availability_window_days"
	SettingKeyMarketplaceAvailabilityBucketMinutes = "marketplace_availability_bucket_minutes"
	SettingKeyReasoningPointRMBUnitPrice           = "reasoning_point_rmb_unit_price"
	SettingKeyUSDExchangeRate                      = "usd_exchange_rate"
)

type ModelMarketplaceGroup struct {
	SearchTerms []string
	Resolution  locale.Resolution
	ID          int64
	Name        string
	Description string

	DisplayBrand               string
	SortOrder                  int
	RateMultiplier             float64
	OfficialPriceRatio         *float64
	OfficialPriceRMBEquivalent *float64
	Capacity                   *GroupCapacitySummary
	Availability               *GroupAvailabilitySummary
	ModelCount                 int
	Models                     []ModelMarketplaceModel
}

type ModelMarketplaceModel struct {
	Attributes  *EffectiveModelAttributes
	ID          string
	DisplayName string
	Pricing     pricing.ModelDisplayPricing
	// InputModalities and OutputModalities use resolved attributes, or pricing metadata when no attribute reader is
	// installed.
	// Nil means the modality is unknown.
	InputModalities  []string
	OutputModalities []string
	// Protocols lists requestable client protocols in provider-resolution order.
	Protocols []capability.ProtocolID
	// NativeProtocols contains protocols supported without conversion on every resolved route.
	NativeProtocols []capability.ProtocolID
}

// MarketplaceListOptions selects optional observations for a marketplace query.
type MarketplaceListOptions struct {
	IncludeCapacity bool
}

type marketplaceDiscountConfig struct {
	reasoningPointRMBUnitPrice float64
	usdExchangeRate            float64
}

type MarketplaceModelDef struct {
	AttributeTargets []modelcatalog.Target
	UpstreamModels   []string
	Protocols        []capability.ProtocolID
	NativeProtocols  []capability.ProtocolID
	ID               string
	DisplayName      string
	PricingModel     string
	PricingAmbiguous bool
}

type MarketplaceGroups interface {
	ListActive(context.Context) ([]Group, error)
}
type MarketplaceSettings interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
}
type MarketplaceModels interface {
	Prefetch(context.Context) ([]CatalogueProvider, bool, error)
	ResolveRequestableModels(context.Context, *int64, string) RequestableModelsResult
}
type MarketplaceCapacity interface {
	GetGroupCapacityByIDs(context.Context, []int64) (map[int64]GroupCapacitySummary, error)
}
type MarketplaceAvailability interface {
	GetSummaryByGroupIDs(context.Context, []int64, int, int, string, time.Time) (map[int64]*GroupAvailabilitySummary, error)
}
type MarketplaceQuoteRequest struct {
	Model              string
	GroupID            int64
	RateMultiplier     float64
	FreeFastApplicable bool
}
type MarketplacePrices interface {
	Quote(context.Context, MarketplaceQuoteRequest) pricing.ModelDisplayPricing
	GetModelModalities(string) ([]string, []string)
}
type MarketplaceOptions struct {
	Attributes func(context.Context, map[int64][]RequestableModel) (map[int64]map[string]EffectiveModelAttributes, error)
	Timezone   string
	Now        func() time.Time
	Warn       func(string, ...any)
}

// Marketplace assembles public models and prices, tolerating failures in auxiliary observations.
type Marketplace struct {
	groups       MarketplaceGroups
	settings     MarketplaceSettings
	models       MarketplaceModels
	requestable  RequestableResolver
	prices       MarketplacePrices
	capacity     MarketplaceCapacity
	availability MarketplaceAvailability
	options      MarketplaceOptions
}

func NewMarketplace(groups MarketplaceGroups, settings MarketplaceSettings, models MarketplaceModels, resolver RequestableResolver, prices MarketplacePrices, capacity MarketplaceCapacity, availability MarketplaceAvailability, options MarketplaceOptions) *Marketplace {
	return &Marketplace{groups: groups, settings: settings, models: models, requestable: resolver, prices: prices, capacity: capacity, availability: availability, options: options}
}

// ListPublic batch-loads visible groups' attributes and optionally includes capacity.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_resolution
func (s *Marketplace) ListPublic(ctx context.Context, options MarketplaceListOptions) ([]ModelMarketplaceGroup, error) {
	groups, err := s.groups.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}

	providersByGroup, providersPrefetched := s.PrefetchProviders(ctx)
	visibleGroups := make([]Group, 0, len(groups))
	definitions := make(map[int64][]MarketplaceModelDef, len(groups))
	for i := range groups {
		group := &groups[i]
		if group.IsExclusive || group.ActiveProviderCount <= 0 {
			continue
		}

		var models []MarketplaceModelDef
		if providersPrefetched {
			models = s.resolveGroupModelsWithProviders(ctx, group, providersByGroup[group.ID])
		} else {
			models = s.resolveGroupModels(ctx, group)
		}
		if len(models) == 0 {
			continue
		}
		visibleGroups = append(visibleGroups, *group)
		definitions[group.ID] = models
	}
	if len(visibleGroups) == 0 {
		return []ModelMarketplaceGroup{}, nil
	}

	attributes := s.readAttributes(ctx, definitions)
	discountConfig, showDiscount := s.getOfficialPriceRatioConfig(ctx)
	var capacityMap map[int64]GroupCapacitySummary
	if options.IncludeCapacity {
		capacityMap = s.getPublicCapacityMap(ctx, visibleGroups)
	}
	availabilityMap := s.getPublicAvailabilityMap(ctx, visibleGroups)
	out := make([]ModelMarketplaceGroup, 0, len(visibleGroups))
	for i := range visibleGroups {
		group := &visibleGroups[i]
		models := s.buildPublicModels(ctx, group, definitions[group.ID], attributes[group.ID])

		var officialPriceRatio *float64
		var officialPriceRMBEquivalent *float64
		if showDiscount {
			officialPriceRatio = discountConfig.officialPriceRatio(group.RateMultiplier)
			officialPriceRMBEquivalent = discountConfig.officialPriceRMBEquivalent(group.RateMultiplier)
		}
		display, resolution := GroupDisplay(group, locale.FromContext(ctx))
		out = append(out, ModelMarketplaceGroup{
			SearchTerms: GroupSearchTexts(group), Resolution: resolution,
			ID:          group.ID,
			Name:        display.DisplayName,
			Description: display.Description,

			DisplayBrand:               marketplaceGroupDisplayBrand(group),
			SortOrder:                  group.SortOrder,
			RateMultiplier:             group.RateMultiplier,
			OfficialPriceRatio:         officialPriceRatio,
			OfficialPriceRMBEquivalent: officialPriceRMBEquivalent,
			Capacity:                   marketplaceGroupCapacity(capacityMap, group.ID),
			Availability:               marketplaceGroupAvailability(availabilityMap, group.ID),
			ModelCount:                 len(models),
			Models:                     models,
		})
	}

	return out, nil
}

// PrefetchProviders reads providers once and orders each group's pool by global priority, then ID.
func (s *Marketplace) PrefetchProviders(ctx context.Context) (map[int64][]CatalogueProvider, bool) {
	if s == nil || s.models == nil {
		return nil, false
	}
	providers, available, err := s.models.Prefetch(ctx)
	if !available && err == nil {
		return nil, false
	}
	if err != nil {
		s.options.Warn("failed to prefetch marketplace providers", "error", err)
		return nil, false
	}

	providersByGroup := make(map[int64][]CatalogueProvider)
	for i := range providers {
		provider := providers[i]
		seenGroups := make(map[int64]struct{}, len(provider.GroupIDs)+len(provider.ProviderGroupIDs))
		for _, providerGroup := range provider.ProviderGroupIDs {
			if providerGroup <= 0 {
				continue
			}
			seenGroups[providerGroup] = struct{}{}
			providersByGroup[providerGroup] = append(providersByGroup[providerGroup], provider)
		}
		for _, groupID := range provider.GroupIDs {
			if groupID <= 0 {
				continue
			}
			if _, exists := seenGroups[groupID]; exists {
				continue
			}
			providersByGroup[groupID] = append(providersByGroup[groupID], provider)
		}
	}

	for groupID := range providersByGroup {
		groupProviders := providersByGroup[groupID]
		sort.SliceStable(groupProviders, func(i, j int) bool {
			if groupProviders[i].Priority != groupProviders[j].Priority {
				return groupProviders[i].Priority < groupProviders[j].Priority
			}
			return groupProviders[i].ID < groupProviders[j].ID
		})
		providersByGroup[groupID] = groupProviders
	}
	return providersByGroup, true
}

func (s *Marketplace) getPublicCapacityMap(ctx context.Context, groups []Group) map[int64]GroupCapacitySummary {
	if s.capacity == nil || len(groups) == 0 {
		return nil
	}

	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		group := &groups[i]
		if group.IsExclusive || group.ActiveProviderCount <= 0 {
			continue
		}
		groupIDs = append(groupIDs, group.ID)
	}
	if len(groupIDs) == 0 {
		return nil
	}

	// Capacity is optional display data; a failed query leaves models and prices available.
	capacityMap, err := s.capacity.GetGroupCapacityByIDs(ctx, groupIDs)
	if err != nil {
		return nil
	}
	return capacityMap
}

func marketplaceGroupCapacity(capacityMap map[int64]GroupCapacitySummary, groupID int64) *GroupCapacitySummary {
	if len(capacityMap) == 0 {
		return nil
	}
	capacity, ok := capacityMap[groupID]
	if !ok {
		return nil
	}
	return &capacity
}

func (s *Marketplace) getPublicAvailabilityMap(ctx context.Context, groups []Group) map[int64]*GroupAvailabilitySummary {
	if s.availability == nil || len(groups) == 0 {
		return nil
	}

	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		group := &groups[i]
		if group.IsExclusive || group.ActiveProviderCount <= 0 {
			continue
		}
		if !group.AvailabilityProbeConfig.Enabled {
			continue
		}
		groupIDs = append(groupIDs, group.ID)
	}
	if len(groupIDs) == 0 {
		return nil
	}

	timezone := "UTC"
	if strings.TrimSpace(s.options.Timezone) != "" {
		timezone = strings.TrimSpace(s.options.Timezone)
	}
	// Probe availability is optional display data; a failed query leaves models and prices available.
	windowDays, bucketMinutes := s.resolveMarketplaceAvailabilityWindow(ctx)
	availabilityMap, err := s.availability.GetSummaryByGroupIDs(ctx, groupIDs, windowDays, bucketMinutes, timezone, s.options.Now())
	if err != nil {
		return nil
	}
	return availabilityMap
}

func (s *Marketplace) resolveMarketplaceAvailabilityWindow(ctx context.Context) (int, int) {
	if s == nil || s.settings == nil {
		return DefaultMarketplaceAvailabilityWindowDays, DefaultMarketplaceAvailabilityBucketMinutes
	}
	settings, err := s.settings.GetMultiple(ctx, []string{
		SettingKeyMarketplaceAvailabilityWindowDays,
		SettingKeyMarketplaceAvailabilityBucketMinutes,
	})
	if err != nil {
		return DefaultMarketplaceAvailabilityWindowDays, DefaultMarketplaceAvailabilityBucketMinutes
	}
	return ParseMarketplaceAvailabilityWindowSettings(settings)
}

func ParseMarketplaceAvailabilityWindowSettings(settings map[string]string) (int, int) {
	windowDays := DefaultMarketplaceAvailabilityWindowDays
	bucketMinutes := DefaultMarketplaceAvailabilityBucketMinutes
	if parsed, err := strconv.Atoi(strings.TrimSpace(settings[SettingKeyMarketplaceAvailabilityWindowDays])); err == nil && parsed > 0 {
		windowDays = parsed
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(settings[SettingKeyMarketplaceAvailabilityBucketMinutes])); err == nil && parsed > 0 {
		bucketMinutes = parsed
	}
	return NormalizeMarketplaceAvailabilityWindow(windowDays, bucketMinutes)
}

func marketplaceGroupAvailability(availabilityMap map[int64]*GroupAvailabilitySummary, groupID int64) *GroupAvailabilitySummary {
	if len(availabilityMap) == 0 {
		return nil
	}
	return availabilityMap[groupID]
}

func marketplaceGroupDisplayBrand(group *Group) string {
	if brand := strings.TrimSpace(group.DisplayBrand); brand != "" {
		return brand
	}
	return group.Name
}

func (c marketplaceDiscountConfig) officialPriceRatio(rateMultiplier float64) *float64 {
	ratio := rateMultiplier * c.reasoningPointRMBUnitPrice / c.usdExchangeRate
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil
	}
	return &ratio
}

func (c marketplaceDiscountConfig) officialPriceRMBEquivalent(rateMultiplier float64) *float64 {
	amount := rateMultiplier * c.reasoningPointRMBUnitPrice
	if amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil
	}
	return &amount
}

func (s *Marketplace) getOfficialPriceRatioConfig(ctx context.Context) (marketplaceDiscountConfig, bool) {
	if s.settings == nil {
		return marketplaceDiscountConfig{}, false
	}

	settings, err := s.settings.GetMultiple(ctx, []string{
		SettingKeyReasoningPointRMBUnitPrice,
		SettingKeyUSDExchangeRate,
	})
	if err != nil {
		return marketplaceDiscountConfig{}, false
	}

	// Showing the official-price discount requires valid unit-price and exchange-rate settings.
	price, priceOK := parsePositiveMarketplaceSettingFloat(settings[SettingKeyReasoningPointRMBUnitPrice])
	exchangeRate, exchangeRateOK := parsePositiveMarketplaceSettingFloat(settings[SettingKeyUSDExchangeRate])
	if !priceOK || !exchangeRateOK {
		return marketplaceDiscountConfig{}, false
	}

	return marketplaceDiscountConfig{
		reasoningPointRMBUnitPrice: price,
		usdExchangeRate:            exchangeRate,
	}, true
}

func parsePositiveMarketplaceSettingFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func (s *Marketplace) ModelsForGroup(ctx context.Context, group *Group) []ModelMarketplaceModel {
	return s.BuildPublicModels(ctx, group, s.resolveGroupModels(ctx, group))
}

// BuildPublicModels resolves display attributes and quotes for one group.
func (s *Marketplace) BuildPublicModels(ctx context.Context, group *Group, modelDefs []MarketplaceModelDef) []ModelMarketplaceModel {
	if len(modelDefs) == 0 {
		return nil
	}

	attributes := s.readAttributes(ctx, map[int64][]MarketplaceModelDef{group.ID: modelDefs})
	return s.buildPublicModels(ctx, group, modelDefs, attributes[group.ID])
}

// readAttributes batch-loads visible models' attributes and returns nil on failure.
func (s *Marketplace) readAttributes(ctx context.Context, definitions map[int64][]MarketplaceModelDef) map[int64]map[string]EffectiveModelAttributes {
	if s.options.Attributes == nil || len(definitions) == 0 {
		return nil
	}
	groups := make(map[int64][]RequestableModel, len(definitions))
	for id, modelDefs := range definitions {
		inputs := make([]RequestableModel, 0, len(modelDefs))
		for _, value := range modelDefs {
			inputs = append(inputs, RequestableModel{ID: value.ID, UpstreamModels: value.UpstreamModels, AttributeTargets: value.AttributeTargets})
		}
		groups[id] = inputs
	}
	attributes, err := s.options.Attributes(ctx, groups)
	if err != nil {
		s.options.Warn("failed to read model attributes", "error", err)
		return nil
	}
	return attributes
}

// buildPublicModels combines loaded attributes with quotes from the shared pricing service.
func (s *Marketplace) buildPublicModels(ctx context.Context, group *Group, modelDefs []MarketplaceModelDef, attributes map[string]EffectiveModelAttributes) []ModelMarketplaceModel {
	models := make([]ModelMarketplaceModel, 0, len(modelDefs))
	for _, modelDef := range modelDefs {
		pricing := pricing.UnknownDisplayPricing()
		if s.prices != nil && !modelDef.PricingAmbiguous {
			pricing = s.RequestableModelPricing(ctx, group, modelDef)
		}
		var inputModalities, outputModalities []string
		var presentation *EffectiveModelAttributes
		if s.options.Attributes != nil {
			if value, ok := attributes[modelDef.ID]; ok {
				presentation = &value
				if value.DisplayName != nil {
					modelDef.DisplayName = *value.DisplayName
				}
				if value.InputModalities != nil {
					inputModalities = *value.InputModalities
				}
				if value.OutputModalities != nil {
					outputModalities = *value.OutputModalities
				}
			}
		} else {
			inputModalities, outputModalities = s.ModelModalities(modelDef)
		}

		models = append(models, ModelMarketplaceModel{
			ID:               modelDef.ID,
			Attributes:       presentation,
			DisplayName:      modelDef.DisplayName,
			Pricing:          pricing,
			InputModalities:  inputModalities,
			OutputModalities: outputModalities,
			Protocols:        modelDef.Protocols,
			NativeProtocols:  modelDef.NativeProtocols,
		})
	}

	return models
}

// ModelModalities reads metadata for the pricing model, using the public ID when no pricing model is set.
func (s *Marketplace) ModelModalities(modelDef MarketplaceModelDef) ([]string, []string) {
	if s.prices == nil {
		return nil, nil
	}
	pricingModel := strings.TrimSpace(modelDef.PricingModel)
	if pricingModel == "" {
		pricingModel = modelDef.ID
	}
	return s.prices.GetModelModalities(pricingModel)
}

// RequestableModelPricing uses the resolver's pricing model so display code does not repeat model mapping.
func (s *Marketplace) RequestableModelPricing(ctx context.Context, group *Group, model MarketplaceModelDef) pricing.ModelDisplayPricing {
	pricingModel := strings.TrimSpace(model.PricingModel)
	if pricingModel == "" {
		pricingModel = model.ID
	}
	return s.PublicModelPricing(ctx, group, pricingModel)
}

// PublicModelPricing requests a quote with the group's rate multiplier and Fast pricing applicability.
func (s *Marketplace) PublicModelPricing(ctx context.Context, group *Group, model string) pricing.ModelDisplayPricing {
	if s.prices == nil {
		return pricing.UnknownDisplayPricing()
	}
	return s.prices.Quote(ctx, MarketplaceQuoteRequest{Model: model, GroupID: group.ID, RateMultiplier: group.RateMultiplier, FreeFastApplicable: true})
}

func (s *Marketplace) resolveGroupModels(ctx context.Context, group *Group) []MarketplaceModelDef {
	if s.models != nil && group != nil {
		groupID := group.ID
		resolution := s.models.ResolveRequestableModels(ctx, &groupID, "")
		if len(resolution.Models) > 0 {
			return buildMarketplaceModelDefsFromRequestable(resolution.Models)
		}
		// An empty resolved catalogue means no eligible models; platform defaults would bypass that result.
		return nil
	}

	if group == nil {
		return nil
	}
	return nil
}

// resolveGroupModelsWithProviders uses prefetched providers to validate request, group-mapped, and upstream model
// stages.
func (s *Marketplace) resolveGroupModelsWithProviders(ctx context.Context, group *Group, providers []CatalogueProvider) []MarketplaceModelDef {
	if s == nil || s.models == nil || group == nil {
		return nil
	}
	groupID := group.ID
	resolution := s.requestable.ResolveWithProviders(ctx, &groupID, "", nil, providers)
	if len(resolution.Models) == 0 {
		return nil
	}
	return buildMarketplaceModelDefsFromRequestable(resolution.Models)
}

func buildMarketplaceModelDefsFromRequestable(models []RequestableModel) []MarketplaceModelDef {
	defs := make([]MarketplaceModelDef, 0, len(models))
	for _, model := range models {
		defs = append(defs, MarketplaceModelDef{
			ID:               model.ID,
			AttributeTargets: model.AttributeTargets,
			UpstreamModels:   model.UpstreamModels,
			Protocols:        model.Protocols,
			NativeProtocols:  model.NativeProtocols,
			DisplayName:      model.ID,
			PricingModel:     model.PricingModel,
			PricingAmbiguous: model.PricingAmbiguous,
		})
	}
	return defs
}
