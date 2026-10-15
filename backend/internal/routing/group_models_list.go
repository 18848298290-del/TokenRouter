package routing

import (
	"strings"
)

// group_models_list.go normalizes ordered custom model lists and reports
// whether they are usable.
// Custom model lists preserve the administrator's selected order and
// distinguish a disabled list from a configured list with usable names.
// Normalization trims blank entries and removes exact duplicates before catalog
// and probe selectors consume the values.

// NormalizeGroupModelsListConfig returns a trimmed list with the first
// occurrence of each case-sensitive model ID. Enabled is retained independently
// of the entries; an empty or wholly blank list has nil Models, and the output
// slice has separate storage when the result contains entries.
func NormalizeGroupModelsListConfig(cfg GroupModelsListConfig) GroupModelsListConfig {
	out := GroupModelsListConfig{Enabled: cfg.Enabled}
	if len(cfg.Models) == 0 {
		return out
	}

	seen := make(map[string]struct{}, len(cfg.Models))
	out.Models = make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out.Models = append(out.Models, model)
	}
	if len(out.Models) == 0 {
		out.Models = nil
	}
	return out
}

// CustomModelsListEnabled reports whether the group enables a list with at
// least one stored entry. Nil groups, disabled lists, and empty lists return
// false; callers normalize model text separately before relying on the entries.
func (g *Group) CustomModelsListEnabled() bool {
	return g != nil && g.ModelsListConfig.Enabled && len(g.ModelsListConfig.Models) > 0
}
