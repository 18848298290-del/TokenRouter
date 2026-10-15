package openaiattempt

import (
	"go.uber.org/zap"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_gateway_reasoning_failover.go derives attempt bodies when reasoning
// replay crosses passthrough modes.
// Encrypted reasoning replay can be valid for a passthrough provider while
// being unsuitable for a later managed conversion. A request remembers whether
// any passthrough attempt occurred, so every subsequent non-passthrough attempt
// derives its input under the cross-mode sanitizer rather than inheriting
// another attempt's edited buffer.
//
// Canonical bytes remain the source for each derivation. Passthrough attempts
// receive those bytes directly, and non-passthrough attempts before passthrough
// was seen also pass through. After that transition, successful sanitizer
// changes produce attempt-local bytes; errors or an unchanged result reuse the
// canonical input. The state survives retries so sanitation is reapplied
// consistently when the request selects another non-passthrough provider.

// openAIPassthroughFailoverState retains passthrough history for one serial
// provider-attempt loop.
type openAIPassthroughFailoverState struct {
	// passthroughSeen becomes true after any passthrough attempt and
	// remains true for later provider retries, including a return to a
	// passthrough provider.
	passthroughSeen bool
}

// deriveOpenAIForwardAttemptBody derives input for the current provider from
// canonical bytes. Callers supply a nonnil provider and request-local state,
// treating canonical input as immutable. Passthrough attempts mark history and
// return that input directly; non-passthrough attempts before that history
// exists also return it.
//
// After passthrough was seen, non-passthrough attempts sanitize encrypted
// reasoning afresh from canonical input. Changed output belongs to this
// attempt. Sanitizer errors are logged when a logger exists and return
// canonical input, while later retries keep the history flag and repeat the
// same derivation rule.
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

	// Derive sanitation from canonical input on every managed-mode attempt,
	// preserving its original content for subsequent passthrough while
	// avoiding reuse of prior edited buffers.
	sanitized, changed, err := openai.SanitizeOpenAICrossModeFailoverReasoning(canonicalBody)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("openai.failover_cross_mode_reasoning_sanitize_failed",
				zap.Int64("provider_id", provider.Record.ID),
				zap.Error(err),
			)
		}
		// Failed sanitation leaves the canonical payload available to
		// forwarding; the retained transition flag still causes later
		// managed retries to attempt sanitation again.
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
