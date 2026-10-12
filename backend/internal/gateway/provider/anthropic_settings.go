package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// GatewayBetaPolicy copies platform beta rules into gateway settings.
func GatewayBetaPolicy(value *anthropic.BetaPolicySettings) *gateway.BetaPolicySettings {
	if value == nil {
		return nil
	}
	result := &gateway.BetaPolicySettings{}
	if value.Rules != nil {
		result.Rules = make([]gateway.BetaPolicyRule, len(value.Rules))
		for i, rule := range value.Rules {
			result.Rules[i] = gateway.BetaPolicyRule(rule)
			result.Rules[i].ModelWhitelist = slices.Clone(rule.ModelWhitelist)
		}
	}
	return result
}

// AnthropicBetaPolicy copies gateway beta rules into the platform execution format.
func AnthropicBetaPolicy(value *gateway.BetaPolicySettings) *anthropic.BetaPolicySettings {
	if value == nil {
		return nil
	}
	result := &anthropic.BetaPolicySettings{}
	if value.Rules != nil {
		result.Rules = make([]anthropic.BetaPolicyRule, len(value.Rules))
		for i, rule := range value.Rules {
			result.Rules[i] = anthropic.BetaPolicyRule(rule)
			result.Rules[i].ModelWhitelist = slices.Clone(rule.ModelWhitelist)
		}
	}
	return result
}
