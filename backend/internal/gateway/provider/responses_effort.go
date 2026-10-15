package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// responses_effort.go reconciles explicit Messages max effort with converted
// effort and final-model capability.
// A Messages request can explicitly ask for max effort even when conversion
// produced another spelling. Reconciliation considers that client intent
// together with final-model capability, so a converter default does not erase
// supported max and a known unsupported max does not reach the outbound request.
//
// Missing capability evidence permits the explicit client value. Known
// unsupported max keeps a different converted effort, or changes converted max
// to xhigh when conversion has not supplied an alternative. Other client effort
// values pass through as converted. The first supplied attributes control the
// decision; upstreamModel remains a compatibility argument with no effect.

// OpenAICompatAnthropicReasoningEffort resolves explicit Messages max effort
// against the first optional attribute set. Requests without an explicit max
// return convertedEffort unchanged; client comparison trims text and ignores
// case.
//
// Unknown or supported max capability returns the client's canonical max,
// even when conversion selected another spelling. Known unsupported max keeps
// convertedEffort unless it also spells max, in which case xhigh supplies the
// compatible result. Later attribute arguments and upstreamModel do not affect
// this decision.
func OpenAICompatAnthropicReasoningEffort(req *protocolanthropic.AnthropicRequest, upstreamModel, convertedEffort string, attributes ...modelcatalog.Attributes) string {
	if req == nil || req.OutputConfig == nil || !strings.EqualFold(strings.TrimSpace(req.OutputConfig.Effort), "max") {
		return convertedEffort
	}
	var attrs modelcatalog.Attributes
	if len(attributes) > 0 {
		attrs = attributes[0]
	}
	if supported := attrs.EffortSupport("max"); supported == nil || *supported {
		return "max"
	}
	// A known unsupported max cannot be forwarded as max; xhigh supplies the
	// compatibility spelling when the converter has not already selected another effort.
	if strings.EqualFold(strings.TrimSpace(convertedEffort), "max") {
		return "xhigh"
	}
	return convertedEffort
}
