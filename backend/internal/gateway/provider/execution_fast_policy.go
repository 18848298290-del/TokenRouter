package provider

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

var openAIFastPolicyCtxKey = openAIFastPolicyCtxKeyType{}

// ExecutionFastPolicy reads Fast settings and prices during request processing.
type ExecutionFastPolicy struct {
	Readers *RuntimeReaders
	Prices  *billing.PriceResolver
}

// openAIFastPolicyCtxKeyType identifies the Fast policy snapshot shared by a WS session.
// Frames reuse the snapshot; policy updates take effect in new sessions.
// Disconnecting the session makes reconnecting requests read the updated policy.
type openAIFastPolicyCtxKeyType struct{}

// Evaluate returns the action and error message for a provider, model, and service tier.
// An unavailable policy reader or no matching rule returns pass.
//
// Matching rules:
//   - Scope selects provider types: all, oauth, apikey, or bedrock.
//   - Nonempty UserIDs matches the trusted API Key owner ID.
//   - ServiceTier is empty, all, or the normalized requested tier.
//   - ModelWhitelist restricts models; FallbackAction handles unmatched models.
//   - User rules precede global rules; each set uses the first match in configuration order.
//
// A request carries one service_tier, and filter removes that field.
// Configuration order resolves overlapping model lists within a scope and tier.
func (s *ExecutionFastPolicy) Evaluate(ctx context.Context, provider *ExecutionProvider, model, serviceTier string) (action, errMsg string) {
	if s == nil || s.Readers == nil {
		return anthropic.BetaPolicyActionPass, ""
	}
	tier := strings.ToLower(strings.TrimSpace(serviceTier))
	if tier == "" {
		return anthropic.BetaPolicyActionPass, ""
	}
	settings := FastPolicySettingsFromContext(ctx)
	if settings == nil {
		fetched, err := s.Readers.Gateway.GetOpenAIFastPolicySettings(ctx)
		if err != nil || fetched == nil {
			return anthropic.BetaPolicyActionPass, ""
		}
		settings = fetched
	}
	return tierpolicy.Evaluate(settings, openAIFastPolicyUserID(ctx), provider != nil && provider.View().IsOAuth(), provider != nil && provider.View().IsBedrock(), model, tier)
}

// openAIFastPolicyUserID reads the trusted API Key owner from request context.
func openAIFastPolicyUserID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	access, _ := apikey.AccessSnapshotFromContext(ctx)
	userID := access.PayerUserID
	if userID <= 0 {
		return 0
	}
	return userID
}

// WithFastPolicyContext binds one settings snapshot for Evaluate calls
// using the context or contexts derived from it.
func WithFastPolicyContext(ctx context.Context, settings *tierpolicy.OpenAIFastPolicySettings) context.Context {
	if ctx == nil || settings == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIFastPolicyCtxKey, settings)
}

func FastPolicySettingsFromContext(ctx context.Context) *tierpolicy.OpenAIFastPolicySettings {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(openAIFastPolicyCtxKey).(*tierpolicy.OpenAIFastPolicySettings); ok {
		return v
	}
	return nil
}

// GroupFastPolicy requires a fully loaded authorized group and an OpenAI provider.
func GroupFastPolicy(ctx context.Context, provider *ExecutionProvider) string {
	if ctx == nil || provider == nil || !provider.View().IsOpenAI() {
		return routing.GroupOpenAIFastPolicyFollowRequest
	}
	group, _ := requeststate.GroupFromContext(ctx)
	if !routing.IsGroupContextValid(group) {
		return routing.GroupOpenAIFastPolicyFollowRequest
	}
	return group.EffectiveOpenAIFastPolicy()
}

func (s *ExecutionFastPolicy) Input(ctx context.Context, value *ExecutionProvider, model string) tierpolicy.DecisionInput {
	return tierpolicy.DecisionInput{
		Model: model, GroupPolicy: GroupFastPolicy(ctx, value), OpenAI: value != nil && value.View().IsOpenAI(),
		Evaluate: func(tier string) (string, string) { return s.Evaluate(ctx, value, model, tier) },
		KeyPolicy: func() string {
			return APIKeyFastModePolicy(ctx)
		},
		ForceOnSupported: func() bool { return s.ForceOnSupported(ctx, value, model) },
	}
}

func (s *ExecutionFastPolicy) ForceOnSupported(ctx context.Context, provider *ExecutionProvider, model string) bool {
	return provider != nil && provider.View().IsOpenAI() &&
		SupportsFastMode(ctx, s.Prices, model)
}
