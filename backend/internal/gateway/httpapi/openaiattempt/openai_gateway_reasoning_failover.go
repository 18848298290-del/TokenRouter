package openaiattempt

import (
	"go.uber.org/zap"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIPassthroughFailoverState records whether this request attempted a passthrough provider.
// Subsequent non-passthrough attempts strip upstream-private encrypted reasoning.
type openAIPassthroughFailoverState struct {
	passthroughSeen bool
}

// deriveOpenAIForwardAttemptBody derives a provider attempt body from immutable canonical input.
// After a passthrough attempt, non-passthrough attempts remove reasoning items with encrypted_content.
// Later non-passthrough retries repeat this derivation; canonical input remains unchanged.
func deriveOpenAIForwardAttemptBody(
	reqLog *zap.Logger,
	canonicalBody []byte,
	provider *gatewayprovider.ExecutionProvider,
	state *openAIPassthroughFailoverState,
) []byte {
	currentPassthrough := provider.View().IsOpenAIPassthroughEnabled()
	if currentPassthrough {
		state.passthroughSeen = true
		return canonicalBody
	}
	if !state.passthroughSeen {
		return canonicalBody
	}

	sanitized, changed, err := openai.SanitizeOpenAICrossModeFailoverReasoning(canonicalBody)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("openai.failover_cross_mode_reasoning_sanitize_failed",
				zap.Int64("provider_id", provider.Record.ID),
				zap.Error(err),
			)
		}
		return canonicalBody
	}
	if !changed {
		return canonicalBody
	}
	if reqLog != nil {
		reqLog.Info("openai.failover_cross_mode_reasoning_stripped",
			zap.Int64("provider_id", provider.Record.ID),
			zap.Bool("provider_passthrough", currentPassthrough),
			zap.Bool("passthrough_seen", true),
		)
	}
	return sanitized
}
