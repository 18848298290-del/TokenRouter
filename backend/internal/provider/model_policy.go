package provider

import (
	"strings"
)

// ModelPlatformRules supplies platform-specific normalization and model eligibility checks.
type ModelPlatformRules struct {
	NormalizeQoder      func(string) string
	QoderCompatible     func(string) bool
	OpenAIOAuthServable func(string) bool
}

// IsModelSupported applies provider mapping before checking the whitelist and platform eligibility.
func (r *Record) IsModelSupported(requestedModel string, defaults ModelMappingDefaults, rules ModelPlatformRules) bool {
	if r == nil {
		return false
	}
	mapping := ResolveModelMapping(r, defaults)
	model, _ := ResolveMappedModel(mapping, requestedModel)
	scope := r.effectiveModelScope(defaults, mapping)
	if !ModelInFinalWhitelist(r.Platform, model, scope, rules.NormalizeQoder) {
		return false
	}
	return r.modelPlatformAllows(model, rules)
}

// modelPlatformAllows checks site and account eligibility for the model.
func (r *Record) modelPlatformAllows(model string, rules ModelPlatformRules) bool {
	if r.Platform == PlatformQoder && rules.QoderCompatible != nil && !rules.QoderCompatible(model) {
		return false
	}
	if r.IsOpenAIOAuth() && rules.OpenAIOAuthServable != nil {
		return rules.OpenAIOAuthServable(model)
	}
	return true
}

// FinalModelWhitelisted checks an already mapped model against the whitelist and Qoder site eligibility.
func (r *Record) FinalModelWhitelisted(model string, defaults ModelMappingDefaults, rules ModelPlatformRules) bool {
	if r == nil {
		return false
	}
	if r.Platform == PlatformQoder && rules.QoderCompatible != nil && !rules.QoderCompatible(model) {
		return false
	}
	return ModelInFinalWhitelist(r.Platform, model, r.effectiveModelScope(defaults, ResolveModelMapping(r, defaults)), rules.NormalizeQoder)
}

// effectiveModelScope treats an empty whitelist as unrestricted except for Spark shadows.
func (r *Record) effectiveModelScope(defaults ModelMappingDefaults, mapping map[string]string) map[string]struct{} {
	whitelist, explicit := ResolveFinalModelWhitelist(r.Platform, r.Credentials, mapping)
	// Spark shadows are limited to their independently quota-limited models, even with a wildcard whitelist.
	if r.IsShadow() && r.Platform == PlatformOpenAI {
		scope := make(map[string]struct{})
		if defaults.Models != nil {
			for _, model := range defaults.Models(r) {
				if len(whitelist) == 0 || ModelInFinalWhitelist(r.Platform, model, whitelist, nil) {
					scope[model] = struct{}{}
				}
			}
		}
		return scope
	}
	if explicit && len(whitelist) > 0 {
		return whitelist
	}
	return map[string]struct{}{"*": {}}
}

// GetConfiguredRequestModels lists concrete configured model names and platform route aliases.
func (r *Record) GetConfiguredRequestModels(defaults ModelMappingDefaults) []string {
	if r == nil {
		return nil
	}
	mapping := ResolveModelMapping(r, defaults)
	scope := r.effectiveModelScope(defaults, mapping)
	var platformModels []string
	if defaults.Models != nil {
		platformModels = defaults.Models(r)
	}
	return configuredRequestModels(mapping, scope, platformModels, func(model string) bool { return ModelInFinalWhitelist(r.Platform, model, scope, nil) })
}

// ResolveMappedModel returns the mapped name and whether an exact or wildcard mapping matched.
// A match is reported even when the target equals the requested name.
func ResolveMappedModel(mapping map[string]string, requestedModel string) (mappedModel string, matched bool) {
	if len(mapping) == 0 {
		return requestedModel, false
	}
	if mappedModel, matched := ResolveRequestedModelInMapping(mapping, requestedModel); matched {
		return mappedModel, true
	}
	normalized := strings.TrimSpace(requestedModel)
	if normalized != requestedModel {
		if mappedModel, matched := ResolveRequestedModelInMapping(mapping, normalized); matched {
			return mappedModel, true
		}
	}
	return requestedModel, false
}

// ModelInFinalWhitelist matches final model names, comparing Qoder aliases by normalized route key.
func ModelInFinalWhitelist(platform, model string, whitelist map[string]struct{}, normalizeQoder func(string) string) bool {
	if len(whitelist) == 0 {
		return false
	}
	model = strings.TrimSpace(model)
	for pattern := range whitelist {
		pattern = strings.TrimSpace(pattern)
		if strings.EqualFold(model, pattern) || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(strings.ToLower(model), strings.ToLower(strings.TrimSuffix(pattern, "*")))) {
			return true
		}
		if platform == PlatformQoder && normalizeQoder != nil && normalizeQoder(pattern) == normalizeQoder(model) {
			return true
		}
	}
	return false
}

// HasUnrestrictedModelScope reports whether the provider's effective scope contains the global wildcard.
func (r *Record) HasUnrestrictedModelScope(defaults ModelMappingDefaults) bool {
	if r == nil {
		return false
	}
	_, all := r.effectiveModelScope(defaults, ResolveModelMapping(r, defaults))["*"]
	return all
}
