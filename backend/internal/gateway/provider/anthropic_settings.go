package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Beta policy settings cross the gateway and Anthropic execution types with
// the same rule fields. Each conversion allocates a rule slice and copies
// model whitelists, so edits to those collections stay with the receiving
// settings.

// GatewayBetaPolicy converts Anthropic beta settings to gateway settings,
// preserving nil settings and nil rule slices. Rules and model whitelist
// slices are copied so the receiving configuration can edit those collections
// independently.
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

// AnthropicBetaPolicy converts gateway beta settings to Anthropic execution
// settings, preserving nil settings and nil rule slices. Rules and model
// whitelist slices are copied before platform-specific request preparation
// uses them.
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
