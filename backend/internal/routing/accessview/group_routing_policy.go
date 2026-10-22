package accessview

import (
	"maps"
	"slices"
)

// group_routing_policy.go defines group model policy and copies its supported
// nested feature values.
// Authentication snapshots reuse group model policy after the administrative
// request finishes. Model mappings, allowlists, and JSON-shaped feature values
// need separate mutable storage so later edits cannot alter a retained
// snapshot.
//
// Feature values can also be supplied directly by Go callers. Copying handles
// map[string]any, []any, boolean and string maps, and string slices recursively
// while retaining nil shapes and scalar types. Other dynamic values pass
// through unchanged, so a caller supplying a pointer or another collection type
// retains responsibility for its mutation and sharing.

// GroupRoutingPolicy contains group model rewrites, restrictions, and feature
// settings independent of prices.
// @project-doc docs/domains/gateway_policy_controls.md#group_routing_policy
type GroupRoutingPolicy struct {
	// Enabled lets readers distinguish active policy from historical inactive
	// drafts. GetGroupPolicy clears inactive routing rules before requests or
	// catalogue queries use them.
	Enabled bool `json:"enabled"`
	// one-step request model rewrites
	ModelMapping map[string]string `json:"model_mapping"`
	// group model allowlist switch
	RestrictModels bool `json:"restrict_models"`
	// RestrictionModelSource selects which model identity the allowlist checks.
	// GetGroupPolicy consumers use group-mapped identity when the field is empty.
	RestrictionModelSource string `json:"restriction_model_source"`
	// AllowedModels is enforced when RestrictModels is true. A configured empty
	// allowlist rejects every model at the selected identity stage.
	AllowedModels []string `json:"allowed_models"`

	Features string `json:"features"` // retained legacy feature text
	// FeaturesConfig stores feature options read by gateway and marketplace consumers.
	// Snapshot copies isolate the map and recognized nested value shapes through
	// DeepCopyFeaturesConfig.
	FeaturesConfig map[string]any `json:"features_config"`
}

// Clone copies model mappings, allowed model names, and supported nested
// feature values. Nil collection shapes remain nil; unsupported dynamic feature
// types retain the original value through DeepCopyFeaturesConfig.
func (p GroupRoutingPolicy) Clone() GroupRoutingPolicy {
	out := p
	out.ModelMapping = maps.Clone(p.ModelMapping)
	out.AllowedModels = slices.Clone(p.AllowedModels)
	out.FeaturesConfig = DeepCopyFeaturesConfig(p.FeaturesConfig)
	return out
}

// DeepCopyFeaturesConfig copies maps and nested feature values supported by
// cloneFeatureValue. Nil input returns nil, and scalar numeric values retain
// their Go types because copying does not serialize them. Callers supply
// acyclic maps and slices; recursive references are not detected.
func DeepCopyFeaturesConfig(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		if inner, ok := value.(map[string]any); ok {
			dst[key] = DeepCopyFeaturesConfig(inner)
		} else {
			dst[key] = cloneFeatureValue(value)
		}
	}
	return dst
}

// cloneFeatureValue recursively copies the recognized feature map and slice
// shapes. Nil typed collections retain their types; values outside those cases
// are returned as supplied and can retain shared references.
func cloneFeatureValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		if v == nil {
			return map[string]any(nil)
		}
		return DeepCopyFeaturesConfig(v)
	case []any:
		if v == nil {
			return []any(nil)
		}
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneFeatureValue(v[i])
		}
		return out
	case map[string]bool:
		return maps.Clone(v)
	case map[string]string:
		return maps.Clone(v)
	case []string:
		return slices.Clone(v)
	default:
		return value
	}
}
