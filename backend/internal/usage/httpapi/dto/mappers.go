package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttpapi "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// mappers.go maps usage records and cleanup tasks into user and administrator
// HTTP responses.
// Usage detail responses display the client model while administrator
// responses add outbound observations and provider costs. Request categories
// also supply older transport flags, so converting a record resolves those
// representations before filling the response.
//
// The mappers keep absent observations absent and restrict administrator
// additions to AdminUsageLog. Billing allocations receive their own entries
// and copied subscription and plan identifiers. Other optional log values,
// image-size maps and timing pointers retain the source references, so
// callers treat those shared values as immutable while a response is being
// encoded.

// usageLogFromServiceUser builds the common usage response from a nonnil
// record and optional display language. It selects RequestedModel when
// present, otherwise Model, and resolves the effective request category
// before deriving the legacy stream and WebSocket flags.
//
// These choices let client-facing names and older transport fields remain
// useful together. Billing allocations are copied by
// cloneBillingAllocationsDTO; other optional pointers and ImageSizeBreakdown
// remain shared with l. Administrator additions are supplied later by
// FromUsageAdmin.
func usageLogFromServiceUser(l *usage.UsageLog, language ...string) UsageLog {
	requestType := l.EffectiveRequestType()
	stream, openAIWSMode := usage.ApplyLegacyRequestFields(requestType, l.Stream, l.OpenAIWSMode)
	requestedModel := l.RequestedModel
	// Historical records can lack the client display name. Use their stored model
	// so both user and administrator responses still identify the recorded request.
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

// cloneBillingAllocationsDTO copies allocation entries and their optional
// SubscriptionID and PlanID values. Empty input returns nil, while each
// populated identifier gets independent storage for the response's billing
// details.
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

// FromUsage builds the regular user's usage response, returning nil for a nil
// record. The response retains optional log pointers and the image-size map;
// callers treat those shared values as immutable during encoding.
func FromUsage(l *usage.UsageLog, language ...string) *UsageLog {
	if l == nil {
		return nil
	}
	u := usageLogFromServiceUser(l, language...)
	return &u
}

// FromUsageAdmin builds an administrator response, returning nil for a nil
// record. It adds outbound observations, provider costs and the provider
// identity; optional source pointers remain shared during response encoding.
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

// TimingFromOps builds the stage-timing response, returning nil when no
// observation is available. Offset and count pointers remain shared with
// timing, so callers leave their values unchanged while encoding the
// response.
func TimingFromOps(timing *ops.OpsRequestTiming) *UsageLogTiming {
	if timing == nil {
		return nil
	}
	return &UsageLogTiming{
		RequestContentLength: timing.RequestContentLength,
		TimingStream:         timing.TimingStream,
		TimingAdmitted:       timing.TimingAdmitted,

		RequestBodyReadMs:       timing.RequestBodyReadMs,
		PreprocessMs:            timing.PreprocessMs,
		UserSlotWaitMs:          timing.UserSlotWaitMs,
		ProviderSlotWaitMs:      timing.ProviderSlotWaitMs,
		RetryProviderSlotWaitMs: timing.RetryProviderSlotWaitMs,

		ProviderSlotAcquiredMs:      timing.ProviderSlotAcquiredMs,
		UpstreamGetConnMs:           timing.UpstreamGetConnMs,
		UpstreamGotConnMs:           timing.UpstreamGotConnMs,
		UpstreamWroteRequestMs:      timing.UpstreamWroteRequestMs,
		UpstreamFirstResponseByteMs: timing.UpstreamFirstResponseByteMs,
		UpstreamFirstEventMs:        timing.UpstreamFirstEventMs,
		UpstreamFirstSSEDataMs:      timing.UpstreamFirstSSEDataMs,
		FirstVisibleOutputMs:        timing.FirstVisibleOutputMs,
		FirstDownstreamFlushMs:      timing.FirstDownstreamFlushMs,

		UpstreamGetConnCount:           timing.UpstreamGetConnCount,
		UpstreamGotConnCount:           timing.UpstreamGotConnCount,
		UpstreamAttemptCount:           timing.UpstreamAttemptCount,
		UpstreamFirstResponseByteCount: timing.UpstreamFirstResponseByteCount,

		UpstreamConnectionReused:  timing.UpstreamConnectionReused,
		UpstreamWroteRequestError: timing.UpstreamWroteRequestError,
	}
}

// CleanupFromUsage builds a cleanup-task response, returning nil for a nil
// task. It maps the DTO's declared filter fields and converts request type to
// text; other filter and lifecycle pointers remain shared.
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

// RequestTypeStringPtr returns a new display-string pointer for a stored
// request category. Nil input returns nil, while invalid stored integers
// normalize to unknown before their lowercase label is produced.
func RequestTypeStringPtr(requestType *int16) *string {
	if requestType == nil {
		return nil
	}
	value := usage.RequestTypeFromInt16(*requestType).String()
	return &value
}
