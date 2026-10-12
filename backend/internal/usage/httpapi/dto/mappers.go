package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttpapi "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

func usageLogFromServiceUser(l *usage.UsageLog, language ...string) UsageLog {
	// Keep provider costs and upstream internals out of user responses; FromUsageAdmin adds those fields.
	requestType := l.EffectiveRequestType()
	stream, openAIWSMode := usage.ApplyLegacyRequestFields(requestType, l.Stream, l.OpenAIWSMode)
	requestedModel := l.RequestedModel
	if requestedModel == "" {
		requestedModel = l.Model
	}
	return UsageLog{
		ID:                        l.ID,
		UserID:                    l.UserID,
		TeamID:                    l.TeamID,
		APIKeyID:                  l.APIKeyID,
		ProviderID:                l.ProviderID,
		Platform:                  l.Platform,
		RequestID:                 l.RequestID,
		Model:                     requestedModel,
		ServiceTier:               l.ServiceTier,
		ReasoningEffort:           l.ReasoningEffort,
		RequestedReasoningEffort:  l.RequestedReasoningEffort,
		InboundEndpoint:           l.InboundEndpoint,
		GroupID:                   l.GroupID,
		SubscriptionID:            l.SubscriptionID,
		InputTokens:               l.InputTokens,
		OutputTokens:              l.OutputTokens,
		CacheCreationTokens:       l.CacheCreationTokens,
		CacheReadTokens:           l.CacheReadTokens,
		CacheCreation5mTokens:     l.CacheCreation5mTokens,
		CacheCreation1hTokens:     l.CacheCreation1hTokens,
		InputCost:                 l.InputCost,
		OutputCost:                l.OutputCost,
		CacheCreationCost:         l.CacheCreationCost,
		CacheReadCost:             l.CacheReadCost,
		TotalCost:                 l.TotalCost,
		ActualCost:                l.ActualCost,
		SubscriptionAmountUSD:     l.SubscriptionAmountUSD,
		BalanceAmountUSD:          l.BalanceAmountUSD,
		BillingAllocations:        cloneBillingAllocationsDTO(l.BillingAllocations),
		RateMultiplier:            l.RateMultiplier,
		LongContextBillingApplied: l.LongContextBillingApplied,
		BillingType:               l.BillingType,
		RequestType:               requestType.String(),
		Stream:                    stream,
		OpenAIWSMode:              openAIWSMode,
		NativeCompactionV2:        l.NativeCompactionV2,
		DurationMs:                l.DurationMs,
		FirstTokenMs:              l.FirstTokenMs,
		ImageCount:                l.ImageCount,
		ImageSize:                 l.ImageSize,
		ImageInputSize:            l.ImageInputSize,
		ImageOutputSize:           l.ImageOutputSize,
		ImageInputTokens:          l.ImageInputTokens,
		ImageInputCost:            l.ImageInputCost,
		ImageOutputTokens:         l.ImageOutputTokens,
		ImageOutputCost:           l.ImageOutputCost,
		ImageSizeSource:           l.ImageSizeSource,
		ImageSizeBreakdown:        l.ImageSizeBreakdown,
		MediaType:                 l.MediaType,
		UserAgent:                 l.UserAgent,
		IPAddress:                 l.IPAddress,
		SessionID:                 l.SessionID,
		CacheTTLOverridden:        l.CacheTTLOverridden,
		BillingMode:               l.BillingMode,
		CreatedAt:                 l.CreatedAt,
		User:                      userFromView(l.User),
		APIKey:                    keyFromView(l.APIKey, language...),
		Group:                     groupFromView(l.Group, language...),
		Subscription:              billinghttpapi.UserSubscriptionFromService(l.Subscription, language...),
	}
}

func cloneBillingAllocationsDTO(allocations []billing.BillingAllocation) []billing.BillingAllocation {
	if len(allocations) == 0 {
		return nil
	}
	out := make([]billing.BillingAllocation, 0, len(allocations))
	for i := range allocations {
		allocation := allocations[i]
		if allocation.SubscriptionID != nil {
			subscriptionID := *allocation.SubscriptionID
			allocation.SubscriptionID = &subscriptionID
		}
		if allocation.PlanID != nil {
			planID := *allocation.PlanID
			allocation.PlanID = &planID
		}
		out = append(out, allocation)
	}
	return out
}

// FromUsage converts a service UsageLog to DTO for regular users.
// Provider costs and upstream internals are reserved for FromUsageAdmin.
func FromUsage(l *usage.UsageLog, language ...string) *UsageLog {
	if l == nil {
		return nil
	}
	u := usageLogFromServiceUser(l, language...)
	return &u
}

// FromUsageAdmin converts a service UsageLog to DTO for admin users.
// It adds provider costs, upstream diagnostics, and provider ID/name, with no provider credentials.
func FromUsageAdmin(l *usage.UsageLog) *AdminUsageLog {
	if l == nil {
		return nil
	}
	usageLog := usageLogFromServiceUser(l)
	usageLog.UpstreamEndpoint = l.UpstreamEndpoint
	return &AdminUsageLog{
		UsageLog:               usageLog,
		UpstreamModel:          l.UpstreamModel,
		UpstreamResponseModel:  l.UpstreamResponseModel,
		UpstreamModelMismatch:  l.UpstreamModelMismatch,
		UpstreamRequestID:      l.UpstreamRequestID,
		PricingConfigID:        l.PricingConfigID,
		ModelMappingChain:      l.ModelMappingChain,
		BillingTier:            l.BillingTier,
		ProviderRateMultiplier: l.ProviderRateMultiplier,
		ProviderStatsCost:      l.ProviderStatsCost,
		IPAddress:              l.IPAddress,
		Provider:               providerFromView(l.Provider),
	}
}

// TimingFromOps copies the stage timings needed by administrator usage views.
func TimingFromOps(timing *ops.OpsRequestTiming) *UsageLogTiming {
	if timing == nil {
		return nil
	}
	return &UsageLogTiming{
		RequestContentLength:           timing.RequestContentLength,
		ProviderSlotAcquiredMs:         timing.ProviderSlotAcquiredMs,
		UpstreamGetConnMs:              timing.UpstreamGetConnMs,
		UpstreamGotConnMs:              timing.UpstreamGotConnMs,
		UpstreamWroteRequestMs:         timing.UpstreamWroteRequestMs,
		UpstreamFirstResponseByteMs:    timing.UpstreamFirstResponseByteMs,
		UpstreamFirstSSEDataMs:         timing.UpstreamFirstSSEDataMs,
		FirstVisibleOutputMs:           timing.FirstVisibleOutputMs,
		FirstDownstreamFlushMs:         timing.FirstDownstreamFlushMs,
		UpstreamGetConnCount:           timing.UpstreamGetConnCount,
		UpstreamGotConnCount:           timing.UpstreamGotConnCount,
		UpstreamAttemptCount:           timing.UpstreamAttemptCount,
		UpstreamFirstResponseByteCount: timing.UpstreamFirstResponseByteCount,
		UpstreamConnectionReused:       timing.UpstreamConnectionReused,
		UpstreamWroteRequestError:      timing.UpstreamWroteRequestError,
	}
}

func CleanupFromUsage(task *usage.UsageCleanupTask) *UsageCleanupTask {
	if task == nil {
		return nil
	}
	return &UsageCleanupTask{
		ID:     task.ID,
		Status: task.Status,
		Filters: UsageCleanupFilters{
			StartTime:   task.Filters.StartTime,
			EndTime:     task.Filters.EndTime,
			UserID:      task.Filters.UserID,
			APIKeyID:    task.Filters.APIKeyID,
			ProviderID:  task.Filters.ProviderID,
			GroupID:     task.Filters.GroupID,
			Model:       task.Filters.Model,
			RequestType: RequestTypeStringPtr(task.Filters.RequestType),
			Stream:      task.Filters.Stream,
			BillingType: task.Filters.BillingType,
		},
		CreatedBy:    task.CreatedBy,
		DeletedRows:  task.DeletedRows,
		ErrorMessage: task.ErrorMsg,
		CanceledBy:   task.CanceledBy,
		CanceledAt:   task.CanceledAt,
		StartedAt:    task.StartedAt,
		FinishedAt:   task.FinishedAt,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
	}
}

func RequestTypeStringPtr(requestType *int16) *string {
	if requestType == nil {
		return nil
	}
	value := usage.RequestTypeFromInt16(*requestType).String()
	return &value
}
