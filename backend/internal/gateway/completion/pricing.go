package completion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (s *Recorder) CalculateRecordUsageCost(
	ctx context.Context,
	result *Result,
	apiKey *KeySnapshot,
	provider *ProviderSnapshot,
	billingModel string,
	requestedModel string,
	billingModelSource string,
	groupMappedModel string,
	multiplier float64,
	imageMultiplier float64,
	opts *PricingOptions,
) *CostBreakdown {
	if opts == nil {
		opts = &PricingOptions{}
	}
	// A configured token billing mode takes precedence over per-image billing.
	if result.ImageCount > 0 {
		if resolved, pricingModel := s.resolveConfigPricingForUsage(ctx, billingModel, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
			return s.CalculateTokenCost(ctx, result, apiKey, provider, billingModel, requestedModel, billingModelSource, groupMappedModel, multiplier, opts)
		} else if resolved != nil {
			return s.CalculateImageCost(ctx, result, apiKey, provider, billingModel, requestedModel, billingModelSource, groupMappedModel, pricingModel, resolved, imageMultiplier, opts.PricingAt)
		}
		if s.imageUsesTokenPricing(billingModel, result.ImageSize, result.Usage) {
			return s.CalculateTokenCost(ctx, result, apiKey, provider, billingModel, requestedModel, billingModelSource, groupMappedModel, multiplier, opts)
		}
		return s.CalculateImageCost(ctx, result, apiKey, provider, billingModel, requestedModel, billingModelSource, groupMappedModel, billingModel, nil, imageMultiplier, opts.PricingAt)
	}

	// Audio uses configured continuous-unit pricing when available, then the general audio price settings.
	if result.AudioUsage != nil {
		resolved, pricingModel := s.resolveConfigPricingForUsage(
			ctx, billingModel, apiKey,
		)
		if resolved != nil && resolved.Mode == BillingModePerRequest {
			gid := apiKey.Group.ID
			cost, err := s.billingService.CalculateCostUnified(CostInput{
				Ctx:     ctx,
				Model:   pricingModel,
				GroupID: &gid,

				UsageUnits:     result.AudioUsage.DurationOrUnits,
				SizeTier:       result.AudioUsage.Mode,
				RateMultiplier: multiplier,
				PricingAt:      opts.PricingAt,
				Resolver:       s.resolver,
				Resolved:       resolved,
			})
			if err == nil {
				return cost
			}
		}
		cfg := groupAudioPriceConfigFromAPIKey(apiKey)
		return s.billingService.CalculateAudioCost(result.AudioUsage.Mode, result.AudioUsage.DurationOrUnits, cfg, multiplier)
	}

	// Search surcharges are added to model token charges.
	tokenCost := s.CalculateTokenCost(ctx, result, apiKey, provider, billingModel, requestedModel, billingModelSource, groupMappedModel, multiplier, opts)
	if result.SearchCount > 0 {
		price := groupSearchPricePer1kFromAPIKey(apiKey)
		if price != nil && *price == 0 {
			s.printf("service.gateway", "[Billing] search_price_per_1k explicit 0; search free group_model=%s count=%d", billingModel, result.SearchCount)
		}
		searchCost := s.billingService.CalculateSearchCost(result.SearchCount, price, multiplier)
		if searchCost != nil && (searchCost.TotalCost > 0 || searchCost.ActualCost > 0) {
			if tokenCost == nil {
				return searchCost
			}
			tokenCost.TotalCost += searchCost.TotalCost
			tokenCost.ActualCost += searchCost.ActualCost
		}
	}
	return tokenCost
}

func (s *Recorder) ResolveConfigPricing(ctx context.Context, billingModel string, apiKey *KeySnapshot) *ResolvedPricing {
	if s.resolver == nil || apiKey == nil || apiKey.Group == nil {
		return nil
	}
	gid := apiKey.Group.ID
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &gid})
	if resolved.HasConfiguredPricing() {
		return resolved
	}
	return nil
}

func (s *Recorder) CalculateImageCost(
	ctx context.Context,
	result *Result,
	apiKey *KeySnapshot,
	provider *ProviderSnapshot,
	billingModel string,
	requestedModel string,
	billingModelSource string,
	groupMappedModel string,
	resolvedModel string,
	resolved *ResolvedPricing,
	multiplier float64,
	pricingAt time.Time,
) *CostBreakdown {
	sizeTier := NormalizeImageBillingTierOrDefault(result.ImageSize)
	if resolved == nil {
		resolved, resolvedModel = s.resolveConfigPricingForUsage(ctx, billingModel, apiKey)
	}
	if resolved != nil {
		tokens := UsageTokens{
			InputTokens:       result.Usage.InputTokens,
			OutputTokens:      result.Usage.OutputTokens,
			ImageOutputTokens: result.Usage.ImageOutputTokens,
		}
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:     ctx,
			Model:   resolvedModel,
			GroupID: &gid,

			Tokens:         tokens,
			RequestCount:   result.ImageCount,
			SizeTier:       sizeTier,
			RateMultiplier: multiplier,
			PricingAt:      pricingAt,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err != nil {
			s.printf("service.gateway", "Calculate image token cost failed: %v", err)
			return &CostBreakdown{ActualCost: 0}
		}
		return cost
	}

	cost, err := s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, multiplier)
	if err != nil {
		s.printf("service.gateway", "Calculate image cost failed: %v", err)
		return &CostBreakdown{BillingMode: string(BillingModeImage)}
	}
	return cost
}

func (s *Recorder) CalculateTokenCost(
	ctx context.Context,
	result *Result,
	apiKey *KeySnapshot,
	provider *ProviderSnapshot,
	billingModel string,
	requestedModel string,
	billingModelSource string,
	groupMappedModel string,
	multiplier float64,
	opts *PricingOptions,
) *CostBreakdown {
	serviceTier := ForwardServiceTier(result)
	tokens := UsageTokens{
		InputTokens:           result.Usage.InputTokens,
		OutputTokens:          result.Usage.OutputTokens,
		CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
		CacheReadTokens:       result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		ImageOutputTokens:     result.Usage.ImageOutputTokens,
	}

	var cost *CostBreakdown
	var err error
	if opts == nil {
		opts = &PricingOptions{}
	}

	// Configured prices determine both the price and the billing model.
	if resolved, resolvedModel := s.resolveConfigPricingForUsage(ctx, billingModel, apiKey); resolved != nil {
		gid := apiKey.Group.ID
		cost, err = s.billingService.CalculateCostUnified(CostInput{
			Ctx:     ctx,
			Model:   resolvedModel,
			GroupID: &gid,

			Tokens:          tokens,
			RequestCount:    1,
			RateMultiplier:  multiplier,
			PricingAt:       opts.PricingAt,
			ServiceTier:     serviceTier,
			ReasoningEffort: stringValueOrEmpty(result.ReasoningEffort),
			Resolver:        s.resolver,
			Resolved:        resolved,
		})
	} else {
		switch {
		case s.resolver != nil && apiKey.Group != nil:
			gid := apiKey.Group.ID
			cost, err = s.billingService.CalculateCostUnified(CostInput{
				Ctx:     ctx,
				Model:   billingModel,
				GroupID: &gid,

				Tokens:          tokens,
				RequestCount:    1,
				RateMultiplier:  multiplier,
				PricingAt:       opts.PricingAt,
				ServiceTier:     serviceTier,
				ReasoningEffort: stringValueOrEmpty(result.ReasoningEffort),
				Resolver:        s.resolver,
			})
		default:
			cost, err = s.billingService.CalculateCostWithServiceTier(billingModel, tokens, multiplier, serviceTier)
			if err == nil {
				price, _ := s.billingService.GetModelPricing(billingModel)
				applyCostBreakdownMultiplier(cost, maxReasoningEffortBillingMultiplier(billingModel, stringValueOrEmpty(result.ReasoningEffort), price))
			}
		}
	}
	if err != nil {
		s.printf("service.gateway", "Calculate cost failed: %v", err)
		return &CostBreakdown{BillingMode: string(BillingModeToken)}
	}
	if cost != nil && cost.BillingMode == "" {
		cost.BillingMode = string(BillingModeToken)
	}
	return cost
}

func (s *Recorder) CalculateOpenAIRecordUsageCostAt(
	ctx context.Context,
	result *Result,
	apiKey *KeySnapshot,
	billingModels []string,
	multiplier float64,
	imageMultiplier float64,
	videoMultiplier float64,
	webSearchMultiplier float64,
	tokens UsageTokens,
	serviceTier string,
	pricingAt time.Time,
) (*CostBreakdown, error) {
	billingModel := firstUsageBillingModel(billingModels)
	if result != nil && result.WebSearchCalls > 0 {
		// Codex alpha/search is billed per call using the configured price; nil defaults to USD 0.01.
		// Search has no token usage. Its base multiplier follows user, group, then system defaults, excluding peak factors.
		return s.billingService.CalculateWebSearchCost(result.WebSearchCalls, webSearchPricePerCallFromAPIKey(apiKey), webSearchMultiplier), nil
	}
	if IsGrokVideoUsageResult(result, billingModels) {
		if resolved := s.ResolveConfigPricing(ctx, billingModel, apiKey); resolved == nil || resolved.Mode != BillingModeToken {
			return s.CalculateOpenAIVideoCost(ctx, billingModel, apiKey, result, videoMultiplier)
		}
	}
	if result != nil && result.AudioUsage != nil {
		if resolved := s.ResolveConfigPricing(ctx, billingModel, apiKey); resolved != nil &&
			(resolved.Mode == BillingModePerRequest) {
			gid := apiKey.Group.ID
			return s.billingService.CalculateCostUnified(CostInput{
				Ctx: ctx, Model: billingModel, GroupID: &gid,
				UsageUnits: result.AudioUsage.DurationOrUnits, SizeTier: result.AudioUsage.Mode,
				RateMultiplier: webSearchMultiplier, Resolver: s.resolver, Resolved: resolved,
			})
		}
		cfg := groupAudioPriceConfigFromAPIKey(apiKey)
		return s.billingService.CalculateAudioCost(result.AudioUsage.Mode, result.AudioUsage.DurationOrUnits, cfg, webSearchMultiplier), nil
	}

	if result != nil && result.ImageCount > 0 {
		// Configured token pricing takes precedence; otherwise use per-image pricing when available.
		resolved := s.ResolveConfigPricing(ctx, billingModel, apiKey)
		if resolved != nil && resolved.Mode != BillingModeToken {
			return s.CalculateOpenAIImageCost(ctx, billingModel, apiKey, result, imageMultiplier)
		}
		if resolved == nil && !s.imageUsesTokenPricing(billingModel, result.ImageSize, result.Usage) {
			return s.CalculateOpenAIImageCost(ctx, billingModel, apiKey, result, imageMultiplier)
		}
	}

	// Compute token costs separately so search charges cannot hide missing token prices.
	var tokenCost *CostBreakdown
	var lastErr error
	if len(billingModels) > 0 && billingModel != "" {
		for _, candidate := range billingModels {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			cost, err := s.CalculateOpenAIRecordUsageTokenCostAt(
				ctx,
				apiKey,
				candidate,
				multiplier,
				pricingAt,
				tokens,
				serviceTier,
				ForwardResultReasoningEffort(result),
			)
			if err == nil {
				tokenCost = cost
				if tokenCost != nil && tokenCost.BillingMode == "" {
					tokenCost.BillingMode = string(BillingModeToken)
				}
				break
			}
			lastErr = err
		}
	}

	var searchCost *CostBreakdown
	if result != nil && result.SearchCount > 0 {
		price := groupSearchPricePer1kFromAPIKey(apiKey)
		if price != nil && *price == 0 {
			s.observeEvent(BillingEvent{Kind: "search_free", SearchCount: result.SearchCount, Model: billingModel, KeyID: apiKey.ID, GroupID: apiKey.GroupID})
		}
		searchCost = s.billingService.CalculateSearchCost(result.SearchCount, price, webSearchMultiplier)
	}

	tokenBillingAttempted := len(billingModels) > 0 && billingModel != ""
	if tokenCost == nil {
		if tokenBillingAttempted {
			if lastErr == nil {
				lastErr = fmt.Errorf("%w: no non-empty billing model candidates", ErrModelPricingUnavailable)
			}
			return nil, fmt.Errorf("calculate OpenAI usage cost failed for billing models %s: %w", strings.Join(billingModels, ","), lastErr)
		}
		// A tool-only request without a model can be billed for search alone.
		if searchCost != nil {
			return searchCost, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("%w: openai usage billing model is empty", ErrModelPricingUnavailable)
		}
		return nil, fmt.Errorf("calculate OpenAI usage cost failed for billing models %s: %w", strings.Join(billingModels, ","), lastErr)
	}
	if searchCost == nil || (searchCost.TotalCost == 0 && searchCost.ActualCost == 0) {
		return tokenCost, nil
	}

	tokenCost.TotalCost += searchCost.TotalCost
	tokenCost.ActualCost += searchCost.ActualCost
	return tokenCost, nil
}

func IsGrokVideoBillingModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video")
}

func IsGrokVideoUsageResult(result *Result, billingModels []string) bool {
	if result == nil || result.VideoCount <= 0 {
		return false
	}
	// A positive VideoCount identifies video completion even after model renaming or mapping.
	// Model-family matches are sufficient but are not required.
	candidates := append([]string{}, billingModels...)
	candidates = append(candidates, result.BillingModel, result.Model, result.UpstreamModel)
	if slices.ContainsFunc(candidates, IsGrokVideoBillingModel) {
		return true
	}
	return true
}

func IsUsagePricingUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrModelPricingUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no pricing available") || strings.Contains(msg, "pricing not found")
}

func (s *Recorder) CalculateOpenAIRecordUsageTokenCostAt(
	ctx context.Context,
	apiKey *KeySnapshot,
	billingModel string,
	multiplier float64,
	pricingAt time.Time,
	tokens UsageTokens,
	serviceTier string,
	reasoningEffort string,
) (*CostBreakdown, error) {
	if s.resolver != nil && apiKey.Group != nil {
		gid := apiKey.Group.ID
		return s.billingService.CalculateCostUnified(CostInput{
			Ctx:     ctx,
			Model:   billingModel,
			GroupID: &gid,

			Tokens:          tokens,
			RequestCount:    1,
			RateMultiplier:  multiplier,
			PricingAt:       pricingAt,
			ServiceTier:     serviceTier,
			ReasoningEffort: reasoningEffort,
			Resolver:        s.resolver,
		})
	}
	return s.billingService.CalculateCostUnified(CostInput{
		Ctx: ctx, Model: billingModel, Tokens: tokens, RateMultiplier: multiplier,
		ServiceTier: serviceTier, ReasoningEffort: reasoningEffort,
	})
}

func ForwardResultReasoningEffort(result *Result) string {
	if result == nil || result.ReasoningEffort == nil {
		return ""
	}
	return *result.ReasoningEffort
}

// imageUsesTokenPricing accepts a model token price when usage is reported and no per-image price exists.
// Fixed per-image reservations still require a per-image price; a token price is not a reservation amount.
func (s *Recorder) imageUsesTokenPricing(model, size string, usage TokenUsage) bool {
	if usage.InputTokens <= 0 && usage.OutputTokens <= 0 && usage.CacheReadInputTokens <= 0 && usage.CacheCreationInputTokens <= 0 && usage.ImageInputTokens <= 0 && usage.ImageOutputTokens <= 0 {
		return false
	}
	if _, err := s.billingService.DefaultImagePrice(model, NormalizeImageBillingTierOrDefault(size)); err == nil {
		return false
	}
	_, err := s.billingService.GetModelPricing(model)
	return err == nil
}

func (s *Recorder) CalculateOpenAIImageCost(ctx context.Context, billingModel string, apiKey *KeySnapshot, result *Result, multiplier float64) (*CostBreakdown, error) {
	sizeTier := NormalizeImageBillingTierOrDefault(result.ImageSize)
	resolved := s.ResolveConfigPricing(ctx, billingModel, apiKey)
	if resolved != nil {
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx: ctx, Model: billingModel, GroupID: apiKey.GroupID,
			RequestCount: result.ImageCount, SizeTier: sizeTier, RateMultiplier: multiplier,
			Resolver: s.resolver, Resolved: resolved,
		})
		if err != nil {
			s.printf("service.openai_gateway", "Calculate image model card cost failed: %v", err)
			return &CostBreakdown{BillingMode: string(resolved.Mode)}, err
		}
		return cost, nil
	}
	cost, err := s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, multiplier)
	if err != nil {
		return &CostBreakdown{BillingMode: string(BillingModeImage)}, err
	}
	return cost, nil
}

func (s *Recorder) CalculateOpenAIVideoCost(ctx context.Context, billingModel string, apiKey *KeySnapshot, result *Result, multiplier float64) (*CostBreakdown, error) {
	videoCount := result.VideoCount
	if videoCount <= 0 {
		videoCount = 1
	}
	resolution := NormalizeVideoBillingResolutionOrDefault(result.VideoResolution)
	durationSeconds := NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
	resolved := s.ResolveConfigPricing(ctx, billingModel, apiKey)
	if resolved != nil {
		units := float64(videoCount)
		if resolved.Mode == BillingModeVideo {
			units *= float64(durationSeconds)
		}
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx: ctx, Model: billingModel, GroupID: apiKey.GroupID,
			RequestCount: videoCount, UsageUnits: units, SizeTier: resolution, RateMultiplier: multiplier,
			Resolver: s.resolver, Resolved: resolved,
		})
		if err != nil {
			s.printf("service.openai_gateway", "Calculate video model card cost failed: %v", err)
			return &CostBreakdown{BillingMode: string(resolved.Mode)}, err
		}
		return cost, nil
	}
	cost, err := s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, multiplier)
	if err != nil {
		return &CostBreakdown{BillingMode: string(BillingModeVideo)}, err
	}
	return cost, nil
}

func (s *Recorder) FilterCNProviderBillingModelCandidates(
	ctx context.Context,
	provider *ProviderSnapshot,
	apiKey *KeySnapshot,
	candidates []string,
) []string {
	if provider == nil || !provider.CNProvider {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if IsCNProviderClaudeFallbackCandidate(candidate) {
			// A Claude fallback candidate for a CN provider requires an effective price override.
			resolved := s.ResolveConfigPricing(ctx, candidate, apiKey)
			if !resolved.HasEffectiveOverridePricing() {
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func IsCNProviderClaudeFallbackCandidate(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "claude") ||
		strings.Contains(model, "opus") ||
		strings.Contains(model, "sonnet") ||
		strings.Contains(model, "haiku")
}

func OpenAIUsageBillingModel(result *Result, fields PricingUsageFields) string {
	if result == nil {
		return ""
	}
	billingModel := ForwardResultBillingModel(result.Model, result.UpstreamModel)
	explicitBillingModel := strings.TrimSpace(result.BillingModel)
	if explicitBillingModel != "" {
		billingModel = explicitBillingModel
	}

	switch fields.BillingModelSource {
	case BillingModelSourceUpstream:
		// Keep the resolved image billing model when the upstream text model describes text processing.
		if upstreamModel := strings.TrimSpace(result.UpstreamModel); upstreamModel != "" && (result.ImageCount <= 0 || explicitBillingModel == "") {
			billingModel = upstreamModel
		}
	case BillingModelSourceGroupMapped:
		mappedModel := strings.TrimSpace(fields.GroupMappedModel)
		if mappedModel != "" && mappedModel != strings.TrimSpace(fields.OriginalModel) {
			billingModel = mappedModel
		}
	case BillingModelSourceRequested:
		if requestedModel := strings.TrimSpace(fields.OriginalModel); requestedModel != "" {
			billingModel = requestedModel
		}
	}
	return billingModel
}

func GroupBillsOpenAIFastAtStandard(apiKey *KeySnapshot, provider *ProviderSnapshot, serviceTier string) bool {
	if apiKey == nil || apiKey.Group == nil || !apiKey.Group.FreeOpenAIFast {
		return false
	}
	if provider == nil || !provider.OpenAI || !apiKey.Group.SupportsOpenAIFast {
		return false
	}
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority", "fast":
		return true
	default:
		return false
	}
}
