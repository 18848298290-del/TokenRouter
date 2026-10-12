package accessview

import (
	"maps"
	"slices"
)

// @project-doc docs/domains/gateway_policy_controls.md#group_routing_policy
// GroupRoutingPolicy contains group routing and feature rules, independently of pricing configuration.
type GroupRoutingPolicy struct {
	Enabled                bool              `json:"enabled"`
	ModelMapping           map[string]string `json:"model_mapping"`
	RestrictModels         bool              `json:"restrict_models"`
	RestrictionModelSource string            `json:"restriction_model_source"`
	AllowedModels          []string          `json:"allowed_models"`
	Features               string            `json:"features"`
	FeaturesConfig         map[string]any    `json:"features_config"`
}

// Clone isolates nested configuration for authentication snapshots and administrative requests.
func (p GroupRoutingPolicy) Clone() GroupRoutingPolicy {
	out := p
	out.ModelMapping = maps.Clone(p.ModelMapping)
	out.AllowedModels = slices.Clone(p.AllowedModels)
	out.FeaturesConfig = DeepCopyFeaturesConfig(p.FeaturesConfig)
	return out
}

// DeepCopyFeaturesConfig copies nested JSON values while retaining their numeric types.
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
