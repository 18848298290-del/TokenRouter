package provider

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// CompletionModels supplies candidate model identifiers for settlement.
type CompletionModels struct{}

// ChatForwardResult converts upstream usage and model names into a completion result.
func ChatForwardResult(result *openai.CompatResponseResult, billingModel string) *forwardcore.OpenAIResult {
	if result == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: result.RequestID, ReasoningEffort: result.ReasoningEffort, ServiceTier: result.ResolvedTier, ResponseID: result.ResponseID, ClientDisconnect: result.ClientDisconnect, UpstreamHeaders: result.UpstreamHeaders, Usage: result.Usage, Model: result.Model, BillingModel: billingModel, UpstreamModel: result.UpstreamModel, UpstreamResponseServiceTier: result.ServiceTier, Stream: result.Stream, Duration: result.Duration, FirstTokenMs: result.FirstTokenMs, SearchCount: result.SearchCount}
}

func (CompletionModels) Candidates(model string, alternates ...string) []string {
	return modelidentity.UsageCandidates(model, alternates...)
}

func ProjectCompletionProvider(v *provider.Record) *completion.ProviderSnapshot {
	if v == nil {
		return nil
	}
	out := &completion.ProviderSnapshot{
		ID:                         v.ID,
		CacheTTLOverrideEnabled:    v.IsCacheTTLOverrideEnabled(),
		CacheTTLOverrideTarget:     v.GetCacheTTLOverrideTarget(),
		AnthropicOAuthOrSetupToken: v.IsAnthropicOAuthOrSetupToken(),
		Type:                       v.Type,
		Platform:                   v.Platform,
		RateMultiplier:             v.BillingRateMultiplier(),
		OpenAI:                     v.IsOpenAI(),
		CNProvider:                 v.IsCNProvider(),
		OAuthLike:                  v.IsOpenAIOAuthLike(),
		QuotaEligible:              v.IsAPIKeyOrBedrock(),
		HasQuotaLimit:              v.HasAnyQuotaLimit(),
		CredentialProviderID:       v.ParentProviderID,
		Notification:               provideradapter.QuotaNotification(&provider.Record{ID: v.ID, Name: v.Name, Platform: v.Platform, Type: v.Type, Extra: v.Extra}),
	}
	return completion.SnapshotProvider(out)
}

func ProjectCompletionKey(v *apikey.APIKey) *completion.KeySnapshot {
	if v == nil {
		return nil
	}

	policy := &apikey.APIKey{
		BillingMode: v.BillingMode,
		RateLimit5h: v.RateLimit5h,
		RateLimit1d: v.RateLimit1d,
		RateLimit7d: v.RateLimit7d,
	}
	out := &completion.KeySnapshot{
		ID:                      v.ID,
		Key:                     v.Key,
		GroupID:                 v.GroupID,
		TeamID:                  v.TeamID,
		PreferredSubscriptionID: v.PreferredSubscriptionID,
		BillingMode:             apikey.APIKeyEffectiveBillingMode(policy),
		Quota:                   v.Quota,
		HasRateLimits:           policy.HasRateLimits(),
	}
	if v.ActorUser != nil {
		out.ActorUserID = v.ActorUser.ID
		out.ActorUserPresent = true
	}
	if g := v.Group; g != nil {
		out.Group = &completion.GroupSnapshot{
			ID: g.ID,

			RateMultiplier:     g.RateMultiplier,
			Location:           time.Local,
			SupportsOpenAIFast: true,
		}
	}
	return completion.SnapshotKey(out)
}

func ProjectCompletionPayer(v *identity.User) *completion.PayerSnapshot {
	if v == nil {
		return nil
	}
	return &completion.PayerSnapshot{ID: v.ID, Balance: v.Balance, Notification: completionUserSummary(v)}
}

func ProjectMessagesCompletionResult(v *forwardcore.MessagesResult, a *provider.Record) *completion.Result {
	if v == nil {
		return nil
	}
	out := &completion.Result{
		RequestID:                   v.RequestID,
		Model:                       v.Model,
		UpstreamModel:               v.UpstreamModel,
		UpstreamRequestID:           usageUpstreamRequestIDPtr(a, v.UpstreamHeaders, false),
		ServiceTier:                 v.ServiceTier,
		UpstreamResponseServiceTier: v.UpstreamResponseServiceTier,
		UpstreamResponseModel:       v.UpstreamResponseModel,
		ReasoningEffort:             v.ReasoningEffort,
		RequestedReasoningEffort:    v.RequestedReasoningEffort,
		Stream:                      v.Stream,
		Duration:                    v.Duration,
		FirstTokenMs:                v.FirstTokenMs,
		ImageCount:                  v.ImageCount,
		ImageSize:                   v.ImageSize,
		ImageInputSize:              v.ImageInputSize,
		ImageOutputSize:             v.ImageOutputSize,
		ImageOutputSizes:            v.ImageOutputSizes,
		ImageSizeSource:             v.ImageSizeSource,
		ImageSizeBreakdown:          v.ImageSizeBreakdown,
		SearchCount:                 v.SearchCount,

		Usage: completion.TokenUsage{
			InputTokens:              v.Usage.InputTokens,
			OutputTokens:             v.Usage.OutputTokens,
			CacheCreationInputTokens: v.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     v.Usage.CacheReadInputTokens,
			CacheCreation5mTokens:    v.Usage.CacheCreation5mTokens,
			CacheCreation1hTokens:    v.Usage.CacheCreation1hTokens,
			ImageOutputTokens:        v.Usage.ImageOutputTokens,
			Speed:                    v.Usage.Speed,
		},
	}
	if v.AudioUsage != nil {
		out.AudioUsage = &completion.AudioUsage{Mode: v.AudioUsage.Mode, DurationOrUnits: v.AudioUsage.DurationOrUnits}
	}
	return completion.SnapshotResult(out)
}

func ProjectOpenAICompletionResult(v *forwardcore.OpenAIResult, a *provider.Record) *completion.Result {
	if v == nil {
		return nil
	}
	out := &completion.Result{
		RequestID:                   v.RequestID,
		ResponseID:                  v.ResponseID,
		Model:                       v.Model,
		BillingModel:                v.BillingModel,
		UpstreamModel:               v.UpstreamModel,
		UpstreamRequestID:           usageUpstreamRequestIDPtr(a, v.UpstreamHeaders, v.OpenAIWSMode),
		ServiceTier:                 v.ServiceTier,
		UpstreamResponseServiceTier: v.UpstreamResponseServiceTier,
		UpstreamResponseModel:       v.UpstreamResponseModel,
		ReasoningEffort:             v.ReasoningEffort,
		RequestedReasoningEffort:    v.RequestedReasoningEffort,
		Stream:                      v.Stream,
		OpenAIWSMode:                v.OpenAIWSMode,
		Duration:                    v.Duration,
		FirstTokenMs:                v.FirstTokenMs,
		ImageCount:                  v.ImageCount,
		ImageSize:                   v.ImageSize,
		ImageInputSize:              v.ImageInputSize,
		ImageOutputSize:             v.ImageOutputSize,
		ImageOutputSizes:            v.ImageOutputSizes,
		ImageSizeSource:             v.ImageSizeSource,
		ImageSizeBreakdown:          v.ImageSizeBreakdown,
		VideoCount:                  v.VideoCount,
		VideoResolution:             v.VideoResolution,
		VideoDurationSeconds:        v.VideoDurationSeconds,
		SearchCount:                 v.SearchCount,
		WebSearchCalls:              v.WebSearchCalls,

		Usage: completion.TokenUsage{
			InputTokens:              v.Usage.InputTokens,
			OutputTokens:             v.Usage.OutputTokens,
			CacheCreationInputTokens: v.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     v.Usage.CacheReadInputTokens,
			ImageInputTokens:         v.Usage.ImageInputTokens,
			ImageOutputTokens:        v.Usage.ImageOutputTokens,
		},
	}
	if v.AudioUsage != nil {
		out.AudioUsage = &completion.AudioUsage{Mode: v.AudioUsage.Mode, DurationOrUnits: v.AudioUsage.DurationOrUnits}
	}
	if v.NativeUsage != nil {
		out.NativeUsage = true
		out.Usage = completion.TokenUsage{InputTokens: v.NativeUsage.InputTokens, OutputTokens: v.NativeUsage.OutputTokens, CacheCreationInputTokens: v.NativeUsage.CacheCreationInputTokens, CacheReadInputTokens: v.NativeUsage.CacheReadInputTokens, CacheCreation5mTokens: v.NativeUsage.CacheCreation5mTokens, CacheCreation1hTokens: v.NativeUsage.CacheCreation1hTokens, ImageOutputTokens: v.NativeUsage.ImageOutputTokens, Speed: v.NativeUsage.Speed}
	}
	return completion.SnapshotResult(out)
}

// completionUserSummary extracts entitlement and notification data from the user record.
func completionUserSummary(u *identity.User) *billing.UserSummary {
	if u == nil {
		return nil
	}
	out := &billing.UserSummary{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		AllowedGroups:              u.AllowedGroups,
		DisabledPublicGroups:       u.DisabledPublicGroups,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RPMLimit,
		APIKeyLimit:                u.APIKeyLimit,
		DeletedAt:                  u.DeletedAt,
	}
	if u.BalanceNotifyExtraEmails != nil {
		out.BalanceNotifyExtraEmails = make([]billing.NotifyEmailSummary, len(u.BalanceNotifyExtraEmails))
		copy(out.BalanceNotifyExtraEmails, u.BalanceNotifyExtraEmails)
	}
	return out
}

// ImagesForwardResult converts image results for delivery and billing.
func ImagesForwardResult(result upstream.AttemptResult, parsed *media.ImageRequest, imageCount int) *forwardcore.OpenAIResult {
	return &forwardcore.OpenAIResult{
		RequestID:       result.RequestID,
		UpstreamHeaders: result.UpstreamHeaders,

		Usage: protocolopenai.ForwardUsage{
			InputTokens:              result.Usage.InputTokens,
			OutputTokens:             result.Usage.OutputTokens,
			CacheReadInputTokens:     result.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: result.Usage.CacheCreationInputTokens,
			ImageInputTokens:         result.ImageInputTokens,
			ImageOutputTokens:        result.Usage.ImageOutputTokens,
		},

		Model:            result.Model,
		UpstreamModel:    result.UpstreamModel,
		Stream:           result.Stream,
		ResponseHeaders:  result.UpstreamHeaders.Clone(),
		Duration:         result.Duration,
		FirstTokenMs:     result.FirstTokenMs,
		ImageCount:       imageCount,
		ImageSize:        parsed.SizeTier,
		ImageInputSize:   parsed.Size,
		ImageOutputSizes: result.ImageOutputSizes,
	}
}
