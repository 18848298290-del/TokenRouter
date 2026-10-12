package ws

import "github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"

// Turn accounting keeps accepted service tier and recorded reasoning effort
// separate from the client's requested effort. RequestUsageDecoder uses the
// shared request-state extractors, so WS snapshots follow the same alias,
// presence, and canonicalization rules used by HTTP completion.

// RequestUsageDecoder delegates per-turn accounting metadata extraction to
// shared request-state rules.
type RequestUsageDecoder struct{}

// ServiceTier extracts the accepted tier for a turn's accounting snapshot. The
// shared extractor normalizes known aliases and returns nil for absent or empty
// metadata; the caller captures it after policy processing.
func (RequestUsageDecoder) ServiceTier(body []byte) *string {
	return requeststate.ExtractOpenAIServiceTierFromBody(body)
}

// ReasoningEffort extracts the recorded effort from the accepted request body.
// Its optional result follows the shared request-state reporting vocabulary;
// callers retain requested effort separately when policy may change it.
func (RequestUsageDecoder) ReasoningEffort(body []byte) *string {
	return requeststate.ExtractOpenAIReasoningEffortFromBody(body)
}

// RequestedReasoningEffort captures canonical client effort before model and
// policy rewriting. The optional value can retain an explicitly requested none,
// letting completion distinguish that choice from omitted request metadata.
func (RequestUsageDecoder) RequestedReasoningEffort(body []byte) *string {
	return requeststate.CanonicalRequestedReasoningEffort(body)
}
