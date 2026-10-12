package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// resolveAntigravityModel applies one mapping; callers check the whitelist after thinking normalization.
func resolveAntigravityModel(value *provider.Record, requested string) string {
	if value == nil {
		return ""
	}
	requested = strings.TrimSpace(strings.TrimPrefix(requested, "models/"))
	if requested == "" {
		return ""
	}
	mapping := provider.ResolveModelMapping(value, ModelDefaults())
	mapped, _ := provider.ResolveMappedModel(mapping, requested)
	return mapped
}

// MapAntigravityModel resolves an allowed model without changing its thinking suffix.
func MapAntigravityModel(value *provider.Record, requested string) string {
	return FinalAntigravityModel(value, requested, nil)
}

// FinalAntigravityModel checks the whitelist after one mapping and thinking normalization.
func FinalAntigravityModel(value *provider.Record, requested string, thinking *bool) string {
	mapped := resolveAntigravityModel(value, requested)
	if mapped == "" {
		return ""
	}
	return finalAntigravityModel(mapped, thinking, func(model string) bool {
		return value.FinalModelWhitelisted(model, ModelDefaults(), ModelRules(value))
	})
}

// FinalAntigravityModelWithRules resolves thinking variants using prepared mappings and a whitelist.
func FinalAntigravityModelWithRules(rules *provider.ModelRulesSnapshot, requested string, thinking *bool) string {
	requested = strings.TrimSpace(strings.TrimPrefix(requested, "models/"))
	if requested == "" {
		return ""
	}
	mapped, _ := rules.ResolveMappedModel(requested)
	return finalAntigravityModel(mapped, thinking, rules.FinalModelWhitelisted)
}

func finalAntigravityModel(mapped string, thinking *bool, allows func(string) bool) string {
	if mapped == "" {
		return ""
	}
	if thinking != nil {
		mapped = antigravity.ApplyThinkingModelSuffix(mapped, *thinking)
	}
	if !allows(mapped) {
		return ""
	}
	return mapped
}
