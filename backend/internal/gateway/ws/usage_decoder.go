package ws

import "github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"

// RequestUsageDecoder parses request tiers and effort for independent per-turn usage snapshots.
type RequestUsageDecoder struct{}

func (RequestUsageDecoder) ServiceTier(body []byte) *string {
	return requeststate.ExtractOpenAIServiceTierFromBody(body)
}

func (RequestUsageDecoder) ReasoningEffort(body []byte) *string {
	return requeststate.ExtractOpenAIReasoningEffortFromBody(body)
}

func (RequestUsageDecoder) RequestedReasoningEffort(body []byte) *string {
	return requeststate.CanonicalRequestedReasoningEffort(body)
}
