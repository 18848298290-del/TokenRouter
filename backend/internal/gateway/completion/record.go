package completion

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

func (s *Recorder) RecordAnthropic(ctx context.Context, input *Input, opts *PricingOptions) error {
	if opts == nil {
		opts = &PricingOptions{}
	}
	result := input.Result
	apiKey := s.keyWithBillingSettings(ctx, input.APIKey)
	user := input.User
	provider := input.Provider
	subscription := input.Subscription
	s.normalizeResult(result, provider, false, provider, input.RequestID)

	// Treat uncached input as cache reads when sticky-session switching requests cache billing.

	if input.ForceCacheBilling && result.Usage.InputTokens > 0 {
		s.printf("service.gateway", "force_cache_billing: %d input_tokens → cache_read_input_tokens (provider=%d)",
			result.Usage.InputTokens, provider.ID)
		result.Usage.CacheReadInputTokens += result.Usage.InputTokens
		result.Usage.InputTokens = 0
	}

	// Provider cache overrides take precedence. Global one-hour injection defaults eligible providers to five-minute billing.
	cacheTTLOverridden := false
	if overrideTarget := s.cacheOverrideTarget(ctx, input); overrideTarget != "" {
		applyCacheOverride(&result.Usage, overrideTarget)
		cacheTTLOverridden = (result.Usage.CacheCreation5mTokens + result.Usage.CacheCreation1hTokens) > 0
	}

	// Balance multipliers prefer user overrides, then group and system defaults.
	multiplier := s.defaultMultiplier
	subscriptionMultiplier := multiplier
	balanceMultiplier := multiplier
	subscription = s.resolveSubscription(ctx, apiKey, subscription, user.ID, apiKey.GroupID)
	if apiKey.GroupID != nil && apiKey.Group != nil {
		groupDefault := apiKey.Group.RateMultiplier
		subscriptionMultiplier = groupDefault
		balanceMultiplier = groupDefault
		if subscription == nil {
			balanceMultiplier = s.resolveUserGroupRateMultiplier(ctx, user.ID, *apiKey.GroupID, groupDefault)
		}
	}
	if apiKey.GroupID != nil && apiKey.Group != nil && subscription == nil {
		multiplier = balanceMultiplier
	} else {
		multiplier = ResolveUsageRateMultiplier(ctx, user.ID, apiKey.GroupID, apiKey.Group, multiplier, subscription, nil)
	}
	// Apply the peak factor to token billing, including image tokens, but not per-image billing.
	// Compute it separately so the user/group multiplier cache does not retain a time-dependent value.
	rateNow := s.now()
	opts.PricingAt = rateNow
	multiplier, imageMultiplier := ComputePeakAwareMultipliers(apiKey, multiplier, rateNow)

	billingModel := ForwardResultBillingModel(result.Model, result.UpstreamModel)
	if input.BillingModelSource == BillingModelSourceUpstream && result.UpstreamModel != "" {
		billingModel = result.UpstreamModel
	}
	if input.BillingModelSource == BillingModelSourceGroupMapped && input.GroupMappedModel != "" {
		billingModel = input.GroupMappedModel
	}
	if input.BillingModelSource == BillingModelSourceRequested && input.OriginalModel != "" {
		billingModel = input.OriginalModel
	}

	// RequestedModel records the model before group mapping.
	requestedModel := result.Model
	if input.OriginalModel != "" {
		requestedModel = input.OriginalModel
	}

	cost := s.CalculateRecordUsageCost(ctx, result, apiKey, provider, billingModel, requestedModel, input.BillingModelSource, input.GroupMappedModel, multiplier, imageMultiplier, opts)

	// The settlement result replaces this provisional billing type.
	isSubscriptionBilling := subscription != nil
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	providerRateMultiplier := provider.RateMultiplier
	usageLog := s.BuildRecordUsageLog(ctx, input, result, apiKey, user, provider, subscription,
		requestedModel, multiplier, imageMultiplier, providerRateMultiplier, billingType, cacheTTLOverridden, cost, opts)

	// Qoder provider-cost rules prefer the requested alias, then the group-mapped and upstream models.
	if apiKey.GroupID != nil {
		s.applyProviderStatsCost(ctx, usageLog,
			provider.ID, *apiKey.GroupID, result.UpstreamModel, requestedModel, input.GroupMappedModel,
			// Anthropic's input_tokens excludes cache_read and cache_creation (billed separately);
			// OpenAI gateway uses actualInputTokens which also excludes cache_read for the same reason.
			UsageTokens{
				InputTokens:         result.Usage.InputTokens,
				OutputTokens:        result.Usage.OutputTokens,
				CacheCreationTokens: result.Usage.CacheCreationInputTokens,
				CacheReadTokens:     result.Usage.CacheReadInputTokens,
				ImageOutputTokens:   result.Usage.ImageOutputTokens,
			},
		)
	}

	subscriptionMultiplier, balanceMultiplier, subscriptionMultiplierScale := RatesForMode(apiKey, cost, subscriptionMultiplier, balanceMultiplier, rateNow)
	_, billingErr := s.Apply(ctx, input.SettlementKey(), usageLog, &usageBillingParams{
		Cost:                            cost,
		User:                            user,
		APIKey:                          apiKey,
		Provider:                        provider,
		Subscription:                    subscription,
		RequestPayloadHash:              input.RequestPayloadHash,
		ProviderRateMultiplier:          providerRateMultiplier,
		SubscriptionRateMultiplier:      subscriptionMultiplier,
		SubscriptionRateMultiplierScale: subscriptionMultiplierScale,
		BalanceRateMultiplier:           balanceMultiplier,
		QuotaUpdates:                    input.QuotaUpdates,
	})

	if billingErr != nil {
		// Keep usage and calculated costs for reconciliation; zero ActualCost marks an unsuccessful settlement.
		usageLog.ActualCost = 0
		s.WriteUsage(ctx, usageLog, "service.gateway")
		return billingErr
	}
	s.WriteUsage(ctx, usageLog, "service.gateway")
	return nil
}

func (s *Recorder) BuildRecordUsageLog(
	ctx context.Context,
	input *Input,
	result *Result,
	apiKey *KeySnapshot,
	user *PayerSnapshot,
	provider *ProviderSnapshot,
	subscription *billing.UserSubscription,
	requestedModel string,
	multiplier float64,
	imageMultiplier float64,
	providerRateMultiplier float64,
	billingType int8,
	cacheTTLOverridden bool,
	cost *CostBreakdown,
	opts *PricingOptions,
) *UsageLog {
	durationMs := int(result.Duration.Milliseconds())
	requestID := input.RequestID
	usageLog := &UsageLog{
		UserID:                actorID(apiKey, user),
		BillingUserID:         user.ID,
		TeamID:                apiKey.TeamID,
		APIKeyID:              apiKey.ID,
		ProviderID:            provider.ID,
		Platform:              provider.Platform,
		RequestID:             requestID,
		BillingKey:            input.SettlementKey(),
		UpstreamRequestID:     result.UpstreamRequestID,
		Model:                 result.Model,
		RequestedModel:        requestedModel,
		UpstreamModel:         OptionalTrimmedStringPtr(result.UpstreamModel),
		UpstreamResponseModel: OptionalTrimmedStringPtr(result.UpstreamResponseModel),
		UpstreamModelMismatch: responseModelMismatch(result),
		ReasoningEffort:       result.ReasoningEffort,
		RequestedReasoningEffort: CoalesceRequestedReasoningEffort(
			result.RequestedReasoningEffort,
			input.RequestedReasoningEffort,
		),
		ServiceTier:            OptionalTrimmedStringPtr(ForwardServiceTier(result)),
		InboundEndpoint:        OptionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:       OptionalTrimmedStringPtr(input.UpstreamEndpoint),
		InputTokens:            result.Usage.InputTokens,
		OutputTokens:           result.Usage.OutputTokens,
		CacheCreationTokens:    result.Usage.CacheCreationInputTokens,
		CacheReadTokens:        result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens:  result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens:  result.Usage.CacheCreation1hTokens,
		ImageOutputTokens:      result.Usage.ImageOutputTokens,
		RateMultiplier:         multiplier,
		ProviderRateMultiplier: &providerRateMultiplier,
		BillingType:            billingType,
		BillingMode:            ResolveBillingMode(result, cost),
		Stream:                 result.Stream,
		DurationMs:             &durationMs,
		FirstTokenMs:           result.FirstTokenMs,
		ImageCount:             result.ImageCount,
		ImageSize:              OptionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:         OptionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:        OptionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:        OptionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:     result.ImageSizeBreakdown,
		CacheTTLOverridden:     cacheTTLOverridden,
		PricingConfigID:        OptionalInt64Ptr(input.PricingConfigID),
		ModelMappingChain:      OptionalTrimmedStringPtr(input.ModelMappingChain),
		UserAgent:              OptionalTrimmedStringPtr(input.UserAgent),
		IPAddress:              OptionalTrimmedStringPtr(input.IPAddress),
		SessionID:              OptionalTrimmedStringPtr(input.ClientSessionID),
		GroupID:                apiKey.GroupID,
		SubscriptionID:         optionalSubscriptionID(subscription),
		CreatedAt:              time.Now(),
	}
	if result.ImageCount > 0 && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = imageMultiplier
	}
	if cost != nil {
		usageLog.InputCost = cost.InputCost
		usageLog.OutputCost = cost.OutputCost
		usageLog.ImageOutputCost = cost.ImageOutputCost
		usageLog.CacheCreationCost = cost.CacheCreationCost
		usageLog.CacheReadCost = cost.CacheReadCost
		usageLog.TotalCost = cost.TotalCost
		usageLog.ActualCost = cost.ActualCost
		usageLog.LongContextBillingApplied = cost.LongContextBillingApplied
	}

	return usageLog
}

func (s *Recorder) RecordOpenAI(ctx context.Context, input *Input) error {
	if input == nil {
		return errors.New("openai usage input is nil")
	}
	result := input.Result
	if result == nil {
		return errors.New("openai usage result is nil")
	}
	if s.health != nil && input.Provider != nil && (input.Provider.OpenAI || input.Provider.CNProvider) {
		s.health.ResetOpenAI403Counter(ctx, input.Provider.ID)
	}
	apiKey, user, provider, subscription := s.keyWithBillingSettings(ctx, input.APIKey), input.User, input.Provider, input.Subscription
	if apiKey == nil || user == nil || provider == nil {
		return errors.New("openai usage input requires api key, user, and provider")
	}
	billingProvider := provider
	if s.providers != nil {
		var err error
		billingProvider, err = s.providers.CredentialProvider(ctx, *provider)
		if err != nil {
			return err
		}
	}
	s.normalizeResult(result, billingProvider, true, provider, input.RequestID)

	// Split OpenAI total input into uncached input, cache reads, and cache creation for separate billing.
	actualInputTokens := max(result.Usage.InputTokens-result.Usage.CacheReadInputTokens-result.Usage.CacheCreationInputTokens, 0)

	tokens := UsageTokens{
		InputTokens:         actualInputTokens,
		ImageInputTokens:    result.Usage.ImageInputTokens,
		OutputTokens:        result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		ImageOutputTokens:   result.Usage.ImageOutputTokens,
	}

	multiplier := s.defaultMultiplier
	subscriptionMultiplier := multiplier
	balanceMultiplier := multiplier
	subscription = s.resolveSubscription(ctx, apiKey, subscription, user.ID, apiKey.GroupID)
	if apiKey.GroupID != nil && apiKey.Group != nil {
		subscriptionMultiplier = apiKey.Group.RateMultiplier
		balanceMultiplier = apiKey.Group.RateMultiplier
		if subscription == nil {
			balanceMultiplier = s.resolveUserGroupRateMultiplier(ctx, user.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
		}
	}
	if apiKey.GroupID != nil && apiKey.Group != nil && subscription == nil {
		multiplier = balanceMultiplier
	} else {
		multiplier = ResolveUsageRateMultiplier(ctx, user.ID, apiKey.GroupID, apiKey.Group, multiplier, subscription, nil)
	}
	// Apply peak factors to token billing, including image tokens, while retaining the per-image base multiplier.
	// Keep time-dependent factors outside the user/group multiplier cache.
	baseMultiplier := multiplier
	rateNow := s.now()
	if !input.PricingAt.IsZero() {
		rateNow = input.PricingAt
	}
	multiplier, imageMultiplier := ComputePeakAwareMultipliers(apiKey, baseMultiplier, rateNow)
	videoMultiplier := baseMultiplier

	var cost *CostBreakdown
	var err error
	billingModel := OpenAIUsageBillingModel(result, input.PricingUsageFields)
	billingModels := s.models.Candidates(billingModel, result.BillingModel, input.GroupMappedModel, input.OriginalModel, result.UpstreamModel, result.Model)
	billingModels = s.FilterCNProviderBillingModelCandidates(ctx, provider, apiKey, billingModels)
	serviceTier := ""
	if result.ServiceTier != nil {
		serviceTier = strings.TrimSpace(*result.ServiceTier)
	}
	cost, err = s.CalculateOpenAIRecordUsageCostAt(
		ctx,
		result,
		apiKey,
		billingModels,
		multiplier,
		imageMultiplier,
		videoMultiplier,
		baseMultiplier,
		tokens,
		serviceTier,
		rateNow,
	)
	if err != nil {
		if !IsUsagePricingUnavailableError(err) {
			return err
		}
		s.observeEvent(BillingEvent{Kind: "pricing_missing", Component: "service.openai_gateway", Models: billingModels, RequestedModel: input.OriginalModel, MappedModel: input.GroupMappedModel, UpstreamModel: result.UpstreamModel, KeyID: apiKey.ID, ProviderID: provider.ID, Err: err})
		// Retain media counts, dimensions, and duration when missing prices make charges zero.
		mode := string(BillingModeToken)
		if cost != nil && cost.BillingMode != "" {
			mode = cost.BillingMode
		}
		cost = &CostBreakdown{BillingMode: mode}
	}

	// Free Fast uses standard prices for user charges while retaining Fast TotalCost for provider statistics.
	// The standard base amount drives subscription and balance allocation.
	var billingBaseAmountUSD *float64
	if GroupBillsOpenAIFastAtStandard(apiKey, billingProvider, serviceTier) && cost != nil {
		standardCost, standardErr := s.CalculateOpenAIRecordUsageCostAt(
			ctx,
			result,
			apiKey,
			billingModels,
			multiplier,
			imageMultiplier,
			videoMultiplier,
			baseMultiplier,
			tokens,
			"",
			rateNow,
		)
		if standardErr != nil {
			if !IsUsagePricingUnavailableError(standardErr) {
				return standardErr
			}
			// Missing standard prices yield zero user charges while provider statistics retain Fast costs.
			s.observeEvent(BillingEvent{Kind: "standard_pricing_missing", Component: "service.openai_gateway", RequestID: input.RequestID, Err: standardErr})
			standardCost = &CostBreakdown{}
		}
		standardBase := standardCost.TotalCost
		billingBaseAmountUSD = &standardBase
		cost.ActualCost = standardCost.ActualCost
	}

	// The settlement result replaces this provisional billing type.
	isSubscriptionBilling := subscription != nil
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	durationMs := int(result.Duration.Milliseconds())
	providerRateMultiplier := provider.RateMultiplier
	requestID := input.RequestID

	// RequestedModel records the model before group mapping.
	requestedModel := result.Model
	if input.OriginalModel != "" {
		requestedModel = input.OriginalModel
	}

	usageLog := &UsageLog{
		UserID:                actorID(apiKey, user),
		BillingUserID:         user.ID,
		TeamID:                apiKey.TeamID,
		APIKeyID:              apiKey.ID,
		ProviderID:            provider.ID,
		Platform:              provider.Platform,
		RequestID:             requestID,
		BillingKey:            input.SettlementKey(),
		UpstreamRequestID:     result.UpstreamRequestID,
		Model:                 result.Model,
		RequestedModel:        requestedModel,
		UpstreamModel:         OptionalTrimmedStringPtr(result.UpstreamModel),
		UpstreamResponseModel: OptionalTrimmedStringPtr(result.UpstreamResponseModel),
		UpstreamModelMismatch: responseModelMismatch(result),
		ServiceTier:           result.ServiceTier,
		ReasoningEffort:       result.ReasoningEffort,
		RequestedReasoningEffort: CoalesceRequestedReasoningEffort(
			result.RequestedReasoningEffort,
			input.RequestedReasoningEffort,
		),
		InboundEndpoint:     OptionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:    OptionalTrimmedStringPtr(input.UpstreamEndpoint),
		InputTokens:         actualInputTokens,
		OutputTokens:        result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		ImageInputTokens:    result.Usage.ImageInputTokens,
		ImageOutputTokens:   result.Usage.ImageOutputTokens,
		ImageCount:          result.ImageCount,
		ImageSize:           OptionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:      OptionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:     OptionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:     OptionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:  result.ImageSizeBreakdown,
	}
	isVideoUsage := IsGrokVideoUsageResult(result, billingModels)
	if isVideoUsage {
		usageLog.VideoCount = result.VideoCount
		usageLog.VideoResolution = OptionalTrimmedStringPtr(NormalizeVideoBillingResolutionOrDefault(result.VideoResolution))
		videoDurationSeconds := NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
		usageLog.VideoDurationSeconds = &videoDurationSeconds
	}
	if cost != nil {
		usageLog.InputCost = cost.InputCost
		usageLog.ImageInputCost = cost.ImageInputCost
		usageLog.OutputCost = cost.OutputCost
		usageLog.ImageOutputCost = cost.ImageOutputCost
		usageLog.CacheCreationCost = cost.CacheCreationCost
		usageLog.CacheReadCost = cost.CacheReadCost
		usageLog.TotalCost = cost.TotalCost
		usageLog.ActualCost = cost.ActualCost
		usageLog.LongContextBillingApplied = cost.LongContextBillingApplied
	}
	if isVideoUsage && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = videoMultiplier
	} else if result.ImageCount > 0 && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = imageMultiplier
	} else {
		usageLog.RateMultiplier = multiplier
	}
	usageLog.ProviderRateMultiplier = &providerRateMultiplier
	usageLog.BillingType = billingType
	usageLog.Stream = result.Stream
	usageLog.NativeCompactionV2 = input.NativeCompactionV2
	if input.CyberBlocked {
		usageLog.RequestType = RequestTypeCyberBlocked
	}
	usageLog.OpenAIWSMode = result.OpenAIWSMode
	usageLog.DurationMs = &durationMs
	usageLog.FirstTokenMs = result.FirstTokenMs
	usageLog.CreatedAt = time.Now()

	usageLog.PricingConfigID = OptionalInt64Ptr(input.PricingConfigID)
	usageLog.ModelMappingChain = OptionalTrimmedStringPtr(input.ModelMappingChain)

	if cost != nil && cost.BillingMode != "" {
		billingMode := cost.BillingMode
		usageLog.BillingMode = &billingMode
	} else if isVideoUsage {
		billingMode := string(BillingModeVideo)
		usageLog.BillingMode = &billingMode
	} else if result.ImageCount > 0 {
		billingMode := string(BillingModeImage)
		usageLog.BillingMode = &billingMode
	} else {
		billingMode := string(BillingModeToken)
		usageLog.BillingMode = &billingMode
	}

	if input.UserAgent != "" {
		usageLog.UserAgent = &input.UserAgent
	}

	if input.IPAddress != "" {
		usageLog.IPAddress = &input.IPAddress
	}

	// Store the captured client session ID; empty values become nil.
	usageLog.SessionID = OptionalTrimmedStringPtr(input.ClientSessionID)

	if apiKey.GroupID != nil {
		usageLog.GroupID = apiKey.GroupID
	}
	if subscription != nil {
		usageLog.SubscriptionID = &subscription.ID
	}

	// Qoder provider-cost rules prefer the requested alias, then the group-mapped and upstream models.
	if apiKey.GroupID != nil {
		s.applyProviderStatsCost(ctx, usageLog,
			provider.ID, *apiKey.GroupID, result.UpstreamModel, requestedModel, input.GroupMappedModel,
			tokens)
	}

	subscriptionMultiplier, balanceMultiplier, subscriptionMultiplierScale := RatesForMode(apiKey, cost, subscriptionMultiplier, balanceMultiplier, rateNow)
	billingErr := func() error {
		_, err := s.Apply(ctx, input.SettlementKey(), usageLog, &usageBillingParams{
			Cost:                            cost,
			User:                            user,
			APIKey:                          apiKey,
			Provider:                        provider,
			Subscription:                    subscription,
			RequestPayloadHash:              input.RequestPayloadHash,
			ProviderRateMultiplier:          providerRateMultiplier,
			SubscriptionRateMultiplier:      subscriptionMultiplier,
			SubscriptionRateMultiplierScale: subscriptionMultiplierScale,
			BalanceRateMultiplier:           balanceMultiplier,
			QuotaUpdates:                    input.QuotaUpdates,
			BillingBaseAmountUSD:            billingBaseAmountUSD,
		})
		return err
	}()

	if billingErr != nil {
		// Keep usage and calculated costs for reconciliation; zero ActualCost marks an unsuccessful settlement.
		usageLog.ActualCost = 0
		s.WriteUsage(ctx, usageLog, "service.openai_gateway")
		return billingErr
	}
	s.WriteUsage(ctx, usageLog, "service.openai_gateway")
	return nil
}

// RecordCyber records eligible error usage through the normal billing path with the Cyber request classification.
// Callers snapshot Input before queue submission.
func (s *Recorder) RecordCyber(ctx context.Context, in *Input) {
	if s == nil || in == nil || in.APIKey == nil || in.User == nil || in.Provider == nil || in.Result == nil || strings.TrimSpace(in.Result.Model) == "" {
		return
	}
	snapshot := Snapshot(in)
	snapshot.Result.Model = strings.TrimSpace(snapshot.Result.Model)
	snapshot.CyberBlocked = true
	if err := s.Record(ctx, snapshot, true); err != nil {
		s.printf("service.openai_gateway", "cyber usage record failed: request_id=%s err=%v", snapshot.RequestID, err)
	}
}
