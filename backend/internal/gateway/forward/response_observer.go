package forward

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// ResponseObserver records upstream model and service-tier declarations separately.
type ResponseObserver struct {
	model protocol.ResponseModelObserver

	firstTier         string
	firstTierConflict bool
	terminalTier      string
}

func (o *ResponseObserver) Observe(model string, terminal bool) {
	if o != nil {
		o.model.Observe(model, terminal)
	}
}

func (o *ResponseObserver) ObserveOpenAI(payload []byte, eventType string) {
	model := firstValidTrimmedGJSONModel(payload, "response.model", "model")
	terminal := isUpstreamResponseModelTerminalEvent(eventType)
	// Tier observations require a model declaration; model-free delta frames
	// cannot establish the tier used for billing.
	if model == "" {
		return
	}
	o.model.ObserveOpenAI(payload, eventType)
	// Typed nonterminal Responses events can echo the requested tier. Accept tier evidence
	// from terminal events and untyped Chat Completions or nonstreaming JSON.
	if !terminal && strings.TrimSpace(eventType) != "" {
		return
	}
	tier := NormalizeObservedOpenAIServiceTier(firstValidTrimmedGJSONModel(payload, "response.service_tier", "service_tier"))
	o.ObserveServiceTier(tier, terminal)
}

func (o *ResponseObserver) ObserveAnthropic(payload []byte) {
	model := firstValidTrimmedGJSONModel(payload, "message.model", "model")
	if model != "" {
		o.Observe(model, false)
	}
	tier := normalizeObservedAnthropicSpeed(firstValidTrimmedGJSONModel(payload, "message.usage.speed", "usage.speed"))
	o.ObserveServiceTier(tier, false)
}

// ObserveServiceTier gives terminal declarations precedence and tracks conflicting nonterminal tiers.
func (o *ResponseObserver) ObserveServiceTier(tier string, terminal bool) {
	if o == nil || tier == "" {
		return
	}
	if terminal {
		o.terminalTier = tier
		return
	}
	if o.firstTier == "" {
		o.firstTier = tier
		return
	}
	if o.firstTier != tier {
		o.firstTierConflict = true
	}
}

// ServiceTier returns the terminal tier, or an unambiguous nonterminal tier; otherwise it returns empty.
func (o *ResponseObserver) ServiceTier() string {
	if o == nil {
		return ""
	}
	if o.terminalTier != "" {
		return o.terminalTier
	}
	if o.firstTierConflict {
		return ""
	}
	return o.firstTier
}

// NormalizeObservedOpenAIServiceTier accepts known tiers and normalizes fast to priority.
func NormalizeObservedOpenAIServiceTier(raw string) string {
	switch value := strings.ToLower(strings.TrimSpace(raw)); value {
	case "priority", "fast":
		return tierpolicy.OpenAIFastTierPriority
	case "default", "flex", "scale":
		return value
	default:
		return ""
	}
}

func normalizeObservedAnthropicSpeed(raw string) string {
	switch value := strings.ToLower(strings.TrimSpace(raw)); value {
	case "fast", "standard":
		return value
	default:
		return ""
	}
}

func (o *ResponseObserver) Model() string {
	if o == nil {
		return ""
	}
	return o.model.Model()
}

func firstValidTrimmedGJSONModel(payload []byte, paths ...string) string {
	return protocol.ResponseModelString(payload, paths...)
}

func isUpstreamResponseModelTerminalEvent(event string) bool {
	return protocol.IsResponseModelTerminalEvent(event)
}

// ResetModel clears model observations before another request, retaining tier observations.
func (o *ResponseObserver) ResetModel() {
	if o != nil {
		o.model = protocol.ResponseModelObserver{}
	}
}

// ObserveOpenAIModel records model declarations for paths that do not collect service tiers.
func (o *ResponseObserver) ObserveOpenAIModel(payload []byte, event string) {
	if o != nil {
		o.model.ObserveOpenAI(payload, event)
	}
}
