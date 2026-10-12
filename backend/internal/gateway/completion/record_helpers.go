package completion

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

func OptionalTrimmedStringPtr(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// CoalesceRequestedReasoningEffort prefers client effort captured by the handler or transport,
// then the unmodified request value. If neither is present, nil records that the client omitted it.
func CoalesceRequestedReasoningEffort(requested, forwarded *string) *string {
	if requested != nil {
		if value := strings.TrimSpace(*requested); value != "" {
			return &value
		}
	}
	if forwarded != nil {
		if value := strings.TrimSpace(*forwarded); value != "" {
			return &value
		}
	}
	return nil
}

func ForwardResultBillingModel(requestedModel, upstreamModel string) string {
	if trimmed := strings.TrimSpace(requestedModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(upstreamModel)
}

func OptionalInt64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func ClaudeServiceTier(speed string) string {
	if strings.EqualFold(strings.TrimSpace(speed), "fast") {
		return "priority"
	}
	return ""
}

func ForwardServiceTier(result *Result) string {
	if result == nil {
		return ""
	}
	if tier := strings.TrimSpace(stringValueOrEmpty(result.ServiceTier)); tier != "" {
		return tier
	}
	return ClaudeServiceTier(result.Usage.Speed)
}

func ResolveBillingMode(result *Result, cost *CostBreakdown) *string {
	var mode string
	switch {
	case cost != nil && cost.BillingMode != "":
		mode = cost.BillingMode
	case result.ImageCount > 0:
		mode = string(BillingModeImage)
	default:
		mode = string(BillingModeToken)
	}
	return &mode
}

func optionalSubscriptionID(subscription *billing.UserSubscription) *int64 {
	if subscription != nil {
		return &subscription.ID
	}
	return nil
}

func RatesForMode(apiKey *KeySnapshot, cost *CostBreakdown, subscriptionBase, balanceBase float64, pricingAt time.Time) (subscriptionRate, balanceRate, scale float64) {
	scale = 1
	if cost == nil || cost.BillingMode == "" || cost.BillingMode == string(BillingModeToken) {
		if apiKey != nil && apiKey.Group != nil {
			scale = apiKey.Group.PeakMultiplierAt(pricingAt)
		}
	}
	return subscriptionBase * scale, balanceBase * scale, scale
}

func RateOrFallback(value, fallback float64) float64 {
	if value > 0 || fallback == 0 {
		return value
	}
	return fallback
}

func SubscriptionPlanIncludesGroup(plan *billing.SubscriptionPlan, groupID int64) bool {
	if plan == nil || groupID <= 0 {
		return false
	}
	if len(plan.GroupIDs) == 0 {
		return true
	}
	return slices.Contains(plan.GroupIDs, groupID)
}

func SubscriptionPlanGroupRateMultiplier(plan *billing.SubscriptionPlan, groupID int64) (float64, bool) {
	if plan == nil || groupID <= 0 {
		return 0, false
	}
	if !SubscriptionPlanIncludesGroup(plan, groupID) {
		return 0, false
	}
	if multiplier, ok := plan.GroupRateMultipliers[groupID]; ok && multiplier > 0 {
		return multiplier, true
	}
	return 0, false
}

func ResolveUsageRateMultiplier(
	ctx context.Context,
	userID int64,
	groupID *int64,
	group *GroupSnapshot,
	defaultMultiplier float64,
	subscription *billing.UserSubscription,
	resolveUserGroupRate func(context.Context, int64, int64, float64) float64,
) float64 {
	multiplier := defaultMultiplier
	if groupID == nil || group == nil {
		return multiplier
	}
	if subscription != nil {
		if multiplier, ok := SubscriptionPlanGroupRateMultiplier(subscription.Plan, *groupID); ok {
			return multiplier
		}
		if SubscriptionPlanIncludesGroup(subscription.Plan, *groupID) {
			return group.RateMultiplier
		}
		return multiplier
	}
	groupDefault := group.RateMultiplier
	if resolveUserGroupRate == nil {
		return groupDefault
	}
	return resolveUserGroupRate(ctx, userID, *groupID, groupDefault)
}

func ComputePeakAwareMultipliers(apiKey *KeySnapshot, base float64, now time.Time) (text, image float64) {
	image = base
	peak := 1.0
	if apiKey != nil && apiKey.Group != nil {
		peak = apiKey.Group.PeakMultiplierAt(now)
	}
	text = base * peak
	return
}

func firstUsageBillingModel(candidates []string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func webSearchPricePerCallFromAPIKey(apiKey *KeySnapshot) *float64 {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return apiKey.Group.WebSearchPricePerCall
}

func groupSearchPricePer1kFromAPIKey(apiKey *KeySnapshot) *float64 {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return apiKey.Group.SearchPricePer1k
}

func groupAudioPriceConfigFromAPIKey(key *KeySnapshot) *pricing.AudioPriceConfig {
	if key == nil || key.Group == nil {
		return nil
	}
	return key.Group.AudioPrice
}

func (s *Recorder) resolveSubscription(ctx context.Context, key *KeySnapshot, current *billing.UserSubscription, userID int64, groupID *int64) *billing.UserSubscription {
	switch key.BillingMode {
	case billing.APIKeyBillingModeBalance:
		return nil
	case billing.APIKeyBillingModeSubscription:
		return current
	}
	return ResolveSubscription(ctx, current, s.subscriptions, userID, groupID)
}

func (s *Recorder) resolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, fallback float64) float64 {
	if s.rates == nil {
		return fallback
	}
	return s.rates.Resolve(ctx, userID, groupID, fallback)
}

func (s *Recorder) resolveConfigPricingForUsage(ctx context.Context, model string, key *KeySnapshot) (*ResolvedPricing, string) {
	return s.ResolveConfigPricing(ctx, model, key), model
}

func actorID(key *KeySnapshot, user *PayerSnapshot) int64 {
	if key.ActorUserPresent || key.ActorUserID != 0 {
		return key.ActorUserID
	}
	return user.ID
}

func stringValueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func stableVideoRequestID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "grok-video:") {
		return id
	}
	return "grok-video:" + id
}

func NormalizeImageBillingTierOrDefault(v string) string {
	return pricing.NormalizeImageBillingTierOrDefault(v)
}

func NormalizeVideoBillingResolutionOrDefault(v string) string {
	return pricing.NormalizeVideoBillingResolutionOrDefault(v)
}

func NormalizeVideoBillingDurationSecondsOrDefault(v int) int {
	return pricing.NormalizeVideoBillingDurationSecondsOrDefault(v)
}

func normalizeBillingServiceTier(v string) string { return pricing.NormalizeBillingServiceTier(v) }

func applyCostBreakdownMultiplier(v *CostBreakdown, m float64) {
	pricing.ApplyCostBreakdownMultiplier(v, m)
}

func maxReasoningEffortBillingMultiplier(model, effort string, p *pricing.ModelPricing) float64 {
	return pricing.MaxReasoningEffortBillingMultiplier(model, effort, p)
}

// cacheOverrideTarget prefers the captured override, then provider settings, reading global policy only when needed.
func (s *Recorder) cacheOverrideTarget(ctx context.Context, input *Input) string {
	if input.CacheOverrideTarget != "" {
		return input.CacheOverrideTarget
	}
	if input.Provider == nil {
		return ""
	}
	if input.Provider.CacheTTLOverrideEnabled {
		return input.Provider.CacheTTLOverrideTarget
	}
	if input.Provider.AnthropicOAuthOrSetupToken && s.cacheInjection != nil && s.cacheInjection.IsAnthropicCacheTTL1hInjectionEnabled(ctx) {
		return "5m"
	}
	return ""
}

func (s *Recorder) observeEvent(event BillingEvent) {
	if s.emit != nil {
		s.emit(event)
	}
}

// ResolveSubscription prefers the supplied subscription and otherwise reads on demand. Callers resolve funding mode first.
func ResolveSubscription(ctx context.Context, current *billing.UserSubscription, reader SubscriptionReader, userID int64, groupID *int64) *billing.UserSubscription {
	if current != nil {
		return current
	}
	if groupID == nil || *groupID <= 0 || userID <= 0 || reader == nil {
		return nil
	}
	sub, err := reader.ResolveUsableSubscriptionForGroup(ctx, userID, *groupID)
	if err != nil {
		return nil
	}
	return sub
}

// keyWithBillingSettings copies the key and reads current shared pricing settings at settlement time.
func (s *Recorder) keyWithBillingSettings(ctx context.Context, key *KeySnapshot) *KeySnapshot {
	key = SnapshotKey(key)
	if key == nil || key.Group == nil {
		return key
	}
	g := key.Group
	settings := s.resolver.BillingSettings(ctx, &g.ID)
	g.PeakRateEnabled, g.PeakStart, g.PeakEnd, g.PeakRateMultiplier = settings.PeakRateEnabled, settings.PeakStart, settings.PeakEnd, settings.PeakRateMultiplier
	g.FreeOpenAIFast = settings.FreeOpenAIFast
	g.WebSearchPricePerCall, g.SearchPricePer1k = settings.WebSearchPricePerCall, settings.SearchPricePer1k
	g.AudioPrice = &pricing.AudioPriceConfig{RealtimePerMin: settings.AudioRealtimePricePerMin, TTSPerMChars: settings.AudioTTSPricePerMillionChars, STTPerHour: settings.AudioSTTPricePerHour}
	return key
}

// applyClientModel writes the captured client model into the usage log requested-model field.
func applyClientModel(ctx context.Context, log *usage.UsageLog) {
	if ctx == nil || log == nil {
		return
	}
	if clientModel, ok := ctx.Value(telemetry.ClientModel).(string); ok && strings.TrimSpace(clientModel) != "" {
		log.RequestedModel = strings.TrimSpace(clientModel)
	}
}

// responseModelMismatch compares the outbound model with the raw upstream declaration; an absent declaration yields nil.
func responseModelMismatch(result *Result) *bool {
	if result == nil || strings.TrimSpace(result.UpstreamResponseModel) == "" {
		return nil
	}
	sent := strings.TrimSpace(result.UpstreamModel)
	if sent == "" {
		sent = strings.TrimSpace(result.Model)
	}
	mismatch := !strings.EqualFold(sent, strings.TrimSpace(result.UpstreamResponseModel))
	return &mismatch
}
