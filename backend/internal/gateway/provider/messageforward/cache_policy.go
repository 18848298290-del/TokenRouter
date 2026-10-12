package messageforward

import (
	"context"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// injectTTL reports whether OAuth or Setup Token requests qualify for configured one-hour cache TTL injection.
func (r *Runtime) injectTTL(ctx context.Context, target *gatewayadapter.ExecutionProvider) bool {
	return target != nil && target.View().IsAnthropicOAuthOrSetupToken() && r.dependencies.Settings != nil && r.dependencies.Settings.IsAnthropicCacheTTL1hInjectionEnabled(ctx)
}

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

func (r *Runtime) rewriteCache(ctx context.Context, body []byte) []byte {
	if r.dependencies.Settings == nil || !r.dependencies.Settings.IsRewriteMessageCacheControlEnabled(ctx) {
		return body
	}
	return anthropic.AddMessageCacheBreakpoints(anthropic.StripMessageCacheControl(body))
}
