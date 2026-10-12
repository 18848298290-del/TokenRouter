package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	purepricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// ModelPricing exposes the pricing package's model rates to billing callers.
type ModelPricing = purepricing.ModelPricing

// UsageTokens describes the token categories used to calculate charges.
type UsageTokens = purepricing.UsageTokens

// CostBreakdown contains per-category costs and the final charge.
type CostBreakdown = purepricing.CostBreakdown

// CostInput supplies usage and pricing context for a charge.
type CostInput struct {
	Ctx             context.Context
	Model           string
	GroupID         *int64 // Group whose shared pricing configuration is queried.
	Tokens          UsageTokens
	RequestCount    int     // Count for per-request billing.
	UsageUnits      float64 // Continuous units, such as audio minutes, hours, or millions of characters.
	SizeTier        string  // Per-request or image tier label, such as "1K", "2K", "4K", or "HD".
	RateMultiplier  float64
	PricingAt       time.Time        // Time used for shared pricing configuration schedules.
	ServiceTier     string           // For example, "priority", "flex", or "".
	ReasoningEffort string           // Forwarded effort; max may select a model or configured multiplier.
	Resolver        *PriceResolver   // Resolves shared pricing configuration and catalog rates.
	Resolved        *ResolvedPricing // Optional resolved prices, reused to skip another Resolve call.
}

// ModelDisplayPricing contains model prices formatted for display.
type ModelDisplayPricing = purepricing.ModelDisplayPricing

// audioPriceConfig contains operation-specific audio rates.
type audioPriceConfig = purepricing.AudioPriceConfig

// PriceCatalog supplies model rates and refreshes the catalog.
type PriceCatalog interface {
	GetModelPricing(string) *purepricing.CatalogModelPricing
	ForceUpdate() error
}

// CalculatorOptions supplies the clock and time zone loader.
type CalculatorOptions struct {
	Now          func() time.Time
	LoadLocation func(string) (*time.Location, error)
}

// Calculator resolves prices and delegates charge calculations to pricing.
type Calculator struct {
	catalog PriceCatalog
	options CalculatorOptions
}

type (
	CatalogModelPricing = purepricing.CatalogModelPricing
	ModelPricingEntry   = purepricing.ModelPricingEntry
)

// applyCostBreakdownMultiplier scales the cost components in place.
func applyCostBreakdownMultiplier(cost *CostBreakdown, multiplier float64) {
	purepricing.ApplyCostBreakdownMultiplier(cost, multiplier)
}

// maxReasoningEffortBillingMultiplier returns the multiplier for max reasoning effort.
func maxReasoningEffortBillingMultiplier(model, effort string, pricing *ModelPricing) float64 {
	return purepricing.MaxReasoningEffortBillingMultiplier(model, effort, pricing)
}

func NewCalculator(catalog PriceCatalog, options CalculatorOptions) *Calculator {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.LoadLocation == nil {
		options.LoadLocation = time.LoadLocation
	}
	return &Calculator{catalog: catalog, options: options}
}

// GetModelPricing resolves model rates from the catalog.
func (s *Calculator) GetModelPricing(model string) (*ModelPricing, error) {
	model = strings.ToLower(model)
	var raw *CatalogModelPricing
	if s.catalog != nil {
		raw = s.catalog.GetModelPricing(model)
	}
	return purepricing.ResolveModelPricing(model, raw)
}

// GetModelPricingWithConfig applies configured overrides to catalog rates, returning catalog lookup errors.
func (s *Calculator) GetModelPricingWithConfig(model string, configPricing *ModelPricingEntry) (*ModelPricing, error) {
	pricing, err := s.GetModelPricing(model)
	if err != nil {
		return nil, err
	}
	return purepricing.ApplyConfigPrice(pricing, configPricing), nil
}

// CalculateCostUnified resolves prices and dispatches by billing mode.
// With no resolver, it calculates token charges directly from the catalog.
func (s *Calculator) CalculateCostUnified(input CostInput) (*CostBreakdown, error) {
	if input.Resolver == nil {
		breakdown, err := s.CalculateCostInternal(input.Model, input.Tokens, input.RateMultiplier, input.ServiceTier, nil)
		if err == nil {
			price, _ := s.GetModelPricing(input.Model)
			applyCostBreakdownMultiplier(breakdown, maxReasoningEffortBillingMultiplier(input.Model, input.ReasoningEffort, price))
		}
		return breakdown, err
	}

	// Reuse prices resolved earlier in the request.
	resolved := input.Resolved
	if resolved == nil {
		resolved = input.Resolver.Resolve(input.Ctx, PricingInput{
			Model:   input.Model,
			GroupID: input.GroupID,
		})
	}

	// Image mode uses size prices. Per-request cards retain label, context interval, and default-price selection.
	if resolved.Mode == purepricing.BillingModeImage {
		price, err := s.resolvedImageUnitPrice(input.Model, input.SizeTier, resolved)
		if err != nil {
			return nil, err
		}
		copy := *resolved
		copy.RequestTiers = nil
		copy.DefaultPerRequestPrice = price
		copy.DefaultPerRequestPricePresent = true
		resolved = &copy
	}
	return purepricing.CalculateCost(resolved, s.ProjectCostInput(input, resolved))
}

// ComputeTokenBreakdown calculates the cost of each token category.
func (s *Calculator) ComputeTokenBreakdown(
	pricing *ModelPricing, tokens UsageTokens,
	rateMultiplier float64, serviceTier string,
	applyLongCtx bool,
) *CostBreakdown {
	return purepricing.ComputeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, applyLongCtx)
}

// CalculateCost calculates token charges using catalog rates.
func (s *Calculator) CalculateCost(model string, tokens UsageTokens, rateMultiplier float64) (*CostBreakdown, error) {
	return s.CalculateCostInternal(model, tokens, rateMultiplier, "", nil)
}

func (s *Calculator) CalculateCostWithServiceTier(model string, tokens UsageTokens, rateMultiplier float64, serviceTier string) (*CostBreakdown, error) {
	return s.CalculateCostInternal(model, tokens, rateMultiplier, serviceTier, nil)
}

func (s *Calculator) CalculateCostInternal(model string, tokens UsageTokens, rateMultiplier float64, serviceTier string, configPricing *ModelPricingEntry) (*CostBreakdown, error) {
	var pricing *ModelPricing
	var err error
	if configPricing != nil {
		pricing, err = s.GetModelPricingWithConfig(model, configPricing)
	} else {
		pricing, err = s.GetModelPricing(model)
	}
	if err != nil {
		return nil, err
	}
	if configPricing == nil {
		pricing = s.applyCatalogTimePricing(pricing, s.options.Now())
	}

	return s.ComputeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, true), nil
}

// ForceUpdatePricing refreshes the catalog, returning an error when no catalog is configured.
func (s *Calculator) ForceUpdatePricing() error {
	if s.catalog != nil {
		return s.catalog.ForceUpdate()
	}
	return fmt.Errorf("pricing service not initialized")
}

// DisplayPricing applies the group multiplier to model display prices.
func (s *Calculator) DisplayPricing(model string, rateMultiplier float64) ModelDisplayPricing {
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}

	if mediaPricing, found := s.imageDisplayPricing(model, rateMultiplier); found {
		return mediaPricing
	}

	pricing, err := s.GetModelPricing(model)
	if err != nil || pricing == nil || !hasAnyDisplayTokenPricing(pricing) {
		return unknownDisplayPricing()
	}

	return buildTokenDisplayPricing(s.applyCatalogTimePricing(pricing, s.options.Now()), rateMultiplier)
}

// DisplayPricingWithResolvedMultipliers prefers resolved shared pricing configuration rates for display.
func (s *Calculator) DisplayPricingWithResolvedMultipliers(model string, rateMultiplier float64, resolved *ResolvedPricing) ModelDisplayPricing {
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	if resolved != nil && resolved.Mode == purepricing.BillingModeImage {
		if quote, ok := s.imageDisplayPricingWithResolved(model, rateMultiplier, resolved); ok {
			return quote
		}
		return unknownDisplayPricing()
	}
	if resolved != nil && resolved.Mode == purepricing.BillingModePerRequest {
		if quote, ok := displayPricingFromResolved(model, rateMultiplier, resolved); ok {
			return quote
		}
		return unknownDisplayPricing()
	}
	if resolved.IsUnpriced() {
		// A model with no token rate may still have a separate per-image catalog price.
		if resolved.ConfigPricing == nil {
			if mediaPricing, found := s.imageDisplayPricing(model, rateMultiplier); found {
				return mediaPricing
			}
		}
		return unknownDisplayPricing()
	}
	if resolved != nil && resolved.Source == PricingSourceCatalog && resolved.BasePricing != nil {
		copy := *resolved
		copy.BasePricing = s.applyCatalogTimePricing(resolved.BasePricing, s.options.Now())
		resolved = &copy
	}
	if pricing, ok := displayPricingFromResolved(model, rateMultiplier, resolved); ok {
		return pricing
	}
	return s.DisplayPricing(model, rateMultiplier)
}

// imageDisplayPricing returns available per-image prices when the model's pricing supports them.
func (s *Calculator) imageDisplayPricing(model string, rateMultiplier float64) (ModelDisplayPricing, bool) {
	return s.imageDisplayPricingWithResolved(model, rateMultiplier, nil)
}

func (s *Calculator) imageDisplayPricingWithResolved(model string, rateMultiplier float64, resolved *ResolvedPricing) (ModelDisplayPricing, bool) {
	raw := s.RawModelPricing(model)
	knownImage := raw != nil && len(raw.ImagePrices) > 0
	if resolved == nil && !knownImage && !hasExplicitImageGenerationPricing(raw) && !looksLikeImageModel(model) && (raw == nil || !raw.TokenPricingAbsent) {
		return ModelDisplayPricing{}, false
	}
	prices := make([]float64, 3)
	found := false
	var sizes []string
	for i, size := range []string{"1K", "2K", "4K"} {
		price, err := purepricing.ResolveImageUnitPrice(resolved, raw, size)
		if err != nil {
			continue
		}
		found = true
		sizes = append(sizes, size)
		prices[i] = price * rateMultiplier
	}
	result := buildImageDisplayPricing(prices[0], prices[1], prices[2])
	result.ImagePriceSizes = sizes
	return result, found
}

// displayPricingFromResolved converts resolved rates to display prices.
func displayPricingFromResolved(model string, rateMultiplier float64, resolved *ResolvedPricing) (ModelDisplayPricing, bool) {
	return purepricing.DisplayPricingFromResolved(model, rateMultiplier, resolved)
}

// buildTokenDisplayPricing applies the multiplier to token display prices.
func buildTokenDisplayPricing(pricing *ModelPricing, rateMultiplier float64) ModelDisplayPricing {
	return purepricing.BuildTokenDisplayPricing(pricing, rateMultiplier)
}

// buildImageDisplayPricing builds display prices for each image size.
func buildImageDisplayPricing(price1K, price2K, price4K float64) ModelDisplayPricing {
	return purepricing.BuildImageDisplayPricing(price1K, price2K, price4K)
}

// unknownDisplayPricing returns a display result marked as unpriced.
func unknownDisplayPricing() ModelDisplayPricing { return purepricing.UnknownDisplayPricing() }

// CalculateWebSearchCost calculates web search charges by call count.
func (s *Calculator) CalculateWebSearchCost(callCount int, groupPrice *float64, rateMultiplier float64) *CostBreakdown {
	return purepricing.CalculateWebSearchCost(callCount, operationPrice("web_search", groupPrice, s.operationPrices().WebSearchPricePerCall), rateMultiplier)
}

// CalculateSearchCost calculates search charges from a price per thousand calls.
func (s *Calculator) CalculateSearchCost(numCalls int, groupPricePer1k *float64, rateMultiplier float64) *CostBreakdown {
	return purepricing.CalculateSearchCost(numCalls, operationPrice("search", groupPricePer1k, s.operationPrices().SearchPricePer1k), rateMultiplier)
}

// CalculateAudioCost calculates charges for the audio mode and its duration or unit count.
func (s *Calculator) CalculateAudioCost(mode string, durationOrUnits float64, groupConfig *audioPriceConfig, rateMultiplier float64) *CostBreakdown {
	return purepricing.CalculateAudioCost(mode, durationOrUnits, s.audioPrices(mode, groupConfig), rateMultiplier)
}

// CalculateImageCost uses the model's per-image price; positive counts require an available price.
func (s *Calculator) CalculateImageCost(model, imageSize string, imageCount int, rateMultiplier float64) (*CostBreakdown, error) {
	if imageCount <= 0 {
		return purepricing.CalculateImageCost(0, imageCount, rateMultiplier), nil
	}
	imageSize = purepricing.NormalizeImageBillingTierOrDefault(imageSize)
	price, err := s.DefaultImagePrice(model, imageSize)
	if err != nil {
		return nil, err
	}
	return purepricing.CalculateImageCost(price, imageCount, rateMultiplier), nil
}

// CalculateVideoCost uses the model's per-second price; positive counts require an available price.
func (s *Calculator) CalculateVideoCost(model, resolution string, videoCount, durationSeconds int, rateMultiplier float64) (*CostBreakdown, error) {
	if videoCount <= 0 {
		return purepricing.CalculateVideoCost(0, videoCount, durationSeconds, rateMultiplier), nil
	}
	resolution = purepricing.NormalizeVideoBillingResolutionOrDefault(resolution)
	durationSeconds = purepricing.NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
	price, err := s.DefaultVideoPrice(model, resolution)
	if err != nil {
		return nil, err
	}
	return purepricing.CalculateVideoCost(price, videoCount, durationSeconds, rateMultiplier), nil
}

// DefaultImagePrice reads the full model name's per-image price, accepting an explicit zero price.
func (s *Calculator) DefaultImagePrice(model, imageSize string) (float64, error) {
	var raw *CatalogModelPricing
	if s.catalog != nil {
		raw = s.catalog.GetModelPricing(model)
	}
	price, found := purepricing.DefaultImagePrice(raw, imageSize)
	if !found {
		return 0, fmt.Errorf("image pricing not found for model %s: %w", model, purepricing.ErrModelPricingUnavailable)
	}
	return purepricing.ValidateImageUnitPrice(price)
}

func (s *Calculator) RawModelPricing(model string) *CatalogModelPricing {
	if s == nil || s.catalog == nil {
		return nil
	}
	return s.catalog.GetModelPricing(model)
}

// hasExplicitImageGenerationPricing reports whether the catalog declares an image generation price.
func hasExplicitImageGenerationPricing(pricing *CatalogModelPricing) bool {
	return purepricing.HasExplicitImageGenerationPricing(pricing)
}

// hasAnyDisplayTokenPricing reports whether token prices are available for display.
func hasAnyDisplayTokenPricing(pricing *ModelPricing) bool {
	return purepricing.HasAnyDisplayTokenPricing(pricing)
}

// looksLikeImageModel classifies image model names heuristically.
func looksLikeImageModel(model string) bool { return purepricing.LooksLikeImageModel(model) }

// DefaultVideoPrice returns the catalog's per-second video rate.
func (s *Calculator) DefaultVideoPrice(model string, resolution string) (float64, error) {
	if price, ok := purepricing.DefaultVideoPrice(s.RawModelPricing(model), resolution); ok {
		return purepricing.ValidateImageUnitPrice(price)
	}
	return 0, fmt.Errorf("video pricing not found for model %s: %w", model, purepricing.ErrModelPricingUnavailable)
}

// ProjectCostInput supplies pricing time zones and uses the injected clock for catalog rules when PricingAt is zero.
func (s *Calculator) ProjectCostInput(input CostInput, resolved *ResolvedPricing) purepricing.CostInput {
	var location *time.Location
	if resolved != nil && resolved.ConfigPricing != nil && resolved.ConfigPricing.TimePricing != nil {
		location, _ = s.options.LoadLocation(resolved.ConfigPricing.TimePricing.Timezone)
	}
	modelAt := input.PricingAt
	if modelAt.IsZero() {
		modelAt = s.options.Now()
	}
	var modelLocation *time.Location
	if resolved != nil && resolved.BasePricing != nil && resolved.BasePricing.TimePricing != nil {
		modelLocation, _ = s.options.LoadLocation(resolved.BasePricing.TimePricing.Timezone)
	}
	return purepricing.CostInput{ModelTimeLocation: modelLocation, Model: input.Model, Tokens: input.Tokens, RequestCount: input.RequestCount, UsageUnits: input.UsageUnits, SizeTier: input.SizeTier, RateMultiplier: input.RateMultiplier, PricingAt: input.PricingAt, ModelPricingAt: modelAt, ServiceTier: input.ServiceTier, ReasoningEffort: input.ReasoningEffort, TimePricingLocation: location}
}

// GetModelModalities queries exact catalog identity metadata; price lookup aliases do not supply modalities.
func (s *Calculator) GetModelModalities(model string) ([]string, []string) {
	if s == nil {
		return nil, nil
	}
	if source, ok := s.catalog.(interface {
		GetModelModalities(string) ([]string, []string)
	}); ok {
		return source.GetModelModalities(model)
	}
	return nil, nil
}
