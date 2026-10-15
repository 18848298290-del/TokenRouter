package messageforward

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// mimic.go adapts system prompts, datelines, and metadata for Anthropic OAuth
// conversion.
// OAuth conversion can move a client system prompt into message history while
// adding the selected model identity prefix. Dateline normalization has
// separate provider eligibility, and metadata injection can defer to configured
// metadata passthrough when the fingerprint service supplies an identity.

// mimicAttempt retains the caller system input for OAuth conversion rewrites.
type mimicAttempt struct {
	// attempt shares the current conversion state with the mimic wrapper,
	// so tool rewrites remain available to the same response output.
	*attempt // current conversion attempt state
	// systemRaw retains the supplied value for the synchronous rewrite; its
	// concrete representation is normalized when RewriteMimicSystem runs.
	systemRaw any // caller system prompt value
}

// shouldNormalizeDateline reports whether the current setting enables
// normalization for Anthropic OAuth or Setup Token requests. Missing targets
// and settings readers disable this path for the caller.
func (r *Runtime) shouldNormalizeDateline(ctx context.Context, target *gatewayadapter.ExecutionProvider) bool {
	return target != nil && target.View().IsAnthropicOAuthOrSetupToken() && r.dependencies.Settings != nil && r.dependencies.Settings.IsClientDatelineNormalizationEnabled(ctx)
}

// normalizeDateline returns rewritten request bytes when an eligible body
// contains a recognized dateline. Disabled eligibility or unchanged text
// returns nil and false, so callers retain their current body.
func (r *Runtime) normalizeDateline(ctx context.Context, target *gatewayadapter.ExecutionProvider, body []byte) ([]byte, bool) {
	if !r.shouldNormalizeDateline(ctx, target) {
		return nil, false
	}
	// Normalize the recognized dateline before forwarding so eligible
	// client fingerprint text uses the upstream normalizer while ordinary
	// user prose keeps its spelling.
	next, _, changed := anthropic.NormalizeDateline(body)
	if !changed {
		return nil, false
	}
	return next, true
}

// RewriteMimicSystem applies model-specific prompt blocks to the retained
// system value and request body. The system input can be a JSON string or array
// accepted by NormalizeSystemParam.
func (a *mimicAttempt) RewriteMimicSystem(body []byte, model, prompt, blocks string) []byte {
	// Select prompt blocks for the mapped model before system rewriting so
	// model- specific identity expansion rules apply to the same request
	// sent upstream.
	blocks = anthropic.ClaudeOAuthSystemPromptBlocksForModel(model, blocks)
	return anthropic.RewriteSystemForNonClaudeCodeWithPromptBlocks(body, anthropic.NormalizeSystemParam(a.systemRaw), prompt, blocks)
}

// MimicMetadata derives missing OAuth metadata after a successful fingerprint
// lookup for the current request. Missing request/fingerprint data and enabled
// metadata passthrough return empty, letting the caller skip metadata
// injection.
func (a *mimicAttempt) MimicMetadata(ctx context.Context, body []byte) string {
	if a.s.dependencies.Fingerprint == nil || !a.c.RequestPresent() {
		return ""
	}
	fp, err := a.s.dependencies.Fingerprint.GetOrCreateFingerprint(ctx, a.provider.Record.ID, a.c.RequestHeaders())
	// A failed or empty fingerprint lookup leaves metadata injection
	// disabled, allowing the prepared request to proceed with its existing
	// identity fields.
	if err != nil || fp == nil {
		return ""
	}
	mimic := false
	if a.s.dependencies.Settings != nil {
		_, mimic, _ = a.s.dependencies.Settings.GetGatewayForwardingSettings(ctx)
	}
	if mimic {
		return ""
	}
	return metadataUserIDFromBody(ctx, a.provider, fp, body)
}

// mimic applies the shared OAuth preparation flow with the conversion attempt
// state supplied by the caller. Tool restoration remains in that state, and
// ineligible targets follow forward.Mimic handling.
func (r *Runtime) mimic(ctx context.Context, output HTTPBoundary, state *AttemptState, target *gatewayadapter.ExecutionProvider, body []byte, system any, model string) []byte {
	a := newAttempt(r, output, target)
	a.state = state
	adapter := &mimicAttempt{attempt: a, systemRaw: system}
	return forward.Mimic(ctx, adapter, target != nil && target.View().IsOAuth(), body, model)
}
