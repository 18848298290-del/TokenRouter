package provider

import (
	"maps"
	"strings"
)

// ModelMappingDefaults supplies platform route aliases and default mappings.
type ModelMappingDefaults struct {
	// Models lists platform route aliases, or the independently quota-limited models for Spark.
	Models      func(*Record) []string
	Antigravity func() map[string]string
}

// ResolveCompactForwardModel applies the Compact mapping, retaining the input for missing or empty targets.
func ResolveCompactForwardModel(value *Record, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || value == nil {
		return model
	}
	mapped, matched := value.ResolveCompactMappedModel(model)
	if matched && strings.TrimSpace(mapped) != "" {
		return strings.TrimSpace(mapped)
	}
	return model
}

// ResolveForwardMappedModel applies one mapping hop and retains the input for missing or empty targets.
func ResolveForwardMappedModel(value *Record, requested string, defaults ModelMappingDefaults) string {
	if value == nil {
		return ""
	}
	mapped, matched := ResolveMappedModel(ResolveModelMapping(value, defaults), requested)
	if !matched || strings.TrimSpace(mapped) == "" {
		return requested
	}
	return strings.TrimSpace(mapped)
}

// ResolveModelMapping returns a separate mapping built from configured entries or platform defaults.
func ResolveModelMapping(a *Record, defaults ModelMappingDefaults) map[string]string {
	rawMapping, _ := a.Credentials["model_mapping"].(map[string]any)
	if a.Credentials == nil {
		if a.Platform == PlatformAntigravity {
			return maps.Clone(defaults.Antigravity())
		}
		// Bedrock forwarding applies defaults together with the region prefix.
		return nil
	}
	if len(rawMapping) == 0 {
		if a.Platform == PlatformAntigravity {
			return maps.Clone(defaults.Antigravity())
		}
		return nil
	}

	result := make(map[string]string)
	for k, v := range rawMapping {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	if len(result) > 0 {
		return result
	}

	if a.Platform == PlatformAntigravity {
		return maps.Clone(defaults.Antigravity())
	}
	return nil
}
