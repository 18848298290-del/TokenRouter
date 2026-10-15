package messageforward

import (
	"context"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// cache_policy.go selects Anthropic cache TTL injection and response usage
// classification.
// Anthropic cache TTL injection and usage classification answer different
// questions: eligible OAuth requests can receive a one-hour request TTL while
// returned cache usage is classified into the five-minute bucket. Provider
// overrides take precedence when the response reader asks how to classify cache
// creation tokens.
//
// The injection flag is read during each decision, so settings invalidation
// affects later requests. Message breakpoint rewriting has its own opt-in flag
// and removes existing message cache controls before adding selected
// breakpoints. The functions return delegated request bytes and leave response
// token extraction to the upstream reader.

// injectTTL reports whether this Anthropic OAuth or Setup Token target
// qualifies for one-hour request cache injection. A missing target or settings
// reader disables injection.
func (r *Runtime) injectTTL(ctx context.Context, target *gatewayadapter.ExecutionProvider) bool {
	return target != nil && target.View().IsAnthropicOAuthOrSetupToken() && r.dependencies.Settings != nil && r.dependencies.Settings.IsAnthropicCacheTTL1hInjectionEnabled(ctx)
}

// cacheUsageOverride selects the cache creation bucket requested by the
// response reader. A provider-specific override wins even when global injection
// is disabled, and a missing provider produces no override.
//
// When an eligible provider instead uses global one-hour injection, this
// function selects the five-minute usage bucket. The returned flag
// distinguishes a selected bucket from the absence of an override, so callers
// retain upstream classification when it is false.
func (r *Runtime) cacheUsageOverride(ctx context.Context, target *gatewayadapter.ExecutionProvider) (string, bool) {
	if target == nil {
		return "", false
	}
	if target.View().IsCacheTTLOverrideEnabled() {
		return target.View().GetCacheTTLOverrideTarget(), true
	}
	if r.injectTTL(ctx, target) {
		return "5m", true
	}
	return "", false
}

// rewriteCache replaces message cache breakpoints when the runtime setting
// enables rewriting. Otherwise it returns the supplied slice, including
// existing client cache annotations and their TTL values.
func (r *Runtime) rewriteCache(ctx context.Context, body []byte) []byte {
	if r.dependencies.Settings == nil || !r.dependencies.Settings.IsRewriteMessageCacheControlEnabled(ctx) {
		return body
	}
	// Remove existing message anchors before selecting replacement
	// breakpoints so client TTL annotations do not remain attached
	// alongside the configured message-cache policy.
	return anthropic.AddMessageCacheBreakpoints(anthropic.StripMessageCacheControl(body))
}
