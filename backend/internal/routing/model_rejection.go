package routing

import (
	"sort"
	"strings"
)

// model_rejection.go builds concrete model suggestions from schedulable
// provider rules.
// A rejection message can suggest concrete model names from schedulable
// providers in its requested platform. Provider configuration supplies those
// names first, and an unconfigured provider can contribute its adapter's
// default list if that read succeeds.
//
// Suggestions are trimmed, filtered through provider support, deduplicated, and
// sorted; wildcard patterns are excluded because a message needs request names.
// The result also distinguishes no configured candidates from a configured list
// whose entries all fail support. The former can return nil, while the latter
// returns an allocated empty list, retaining information useful to callers
// observing result shape.

// ModelRejectionRules supplies scheduling eligibility and configured model
// checks for rejection suggestions.
type ModelRejectionRules interface {
	// IsSchedulable reports whether this provider may contribute rejection
	// suggestions.
	IsSchedulable() bool
	// GetConfiguredRequestModels returns configured request names for this
	// provider's diagnostic scope. An empty list lets AvailableModelsForRejection
	// ask the optional defaults adapter for concrete candidates used in rejection
	// messages.
	GetConfiguredRequestModels() []string
	// IsModelSupported reports whether a candidate passes this provider's
	// configured model rules. Suggestion building calls it after removing blank
	// names and wildcard patterns from either configured or default lists.
	IsModelSupported(string) bool
}

// ModelRejectionSource binds a provider's platform, rule reader, and optional
// defaults lookup.
type ModelRejectionSource struct {
	Platform string              // configured provider platform name
	Rules    ModelRejectionRules // required lazy model rules
	// Defaults supplies candidates when this provider has no configured request
	// models. Errors are ignored by suggestion building, leaving the provider with
	// no suggested names.
	Defaults func(string) ([]string, error)
}

// AvailableModelsForRejection returns sorted concrete suggestions from eligible
// providers matching the optional platform. Candidate lists and support checks
// are read lazily after scheduling and platform filters pass.
//
// An unconfigured provider can use its defaults callback, and callback failures
// contribute no suggestions. Blank names, wildcard patterns, and unsupported
// models are excluded from both sources. If no concrete name remains,
// configured candidate presence still distinguishes an allocated empty result
// from nil when every contributing provider was unconfigured.
func AvailableModelsForRejection(providers []ModelRejectionSource, platform string) []string {
	modelSet := make(map[string]struct{})
	hasConfiguredModels := false
	for i := range providers {
		value := &providers[i]
		if !value.Rules.IsSchedulable() || !matchesRejectionPlatform(value, platform) {
			continue
		}
		requestModels := value.Rules.GetConfiguredRequestModels()
		// Default candidates are read only for an unconfigured eligible provider. A
		// configured list that filters to empty still records administrator-supplied
		// scope.
		if len(requestModels) == 0 {
			if value.Defaults == nil {
				continue
			}
			defaultModels, err := value.Defaults(value.Platform)
			if err != nil {
				// Default catalogue failure should not replace the original model
				// rejection; omit this provider's suggestions and continue inspecting the
				// remaining pool.
				continue
			}
			for _, model := range defaultModels {
				if model = strings.TrimSpace(model); model != "" && !strings.Contains(model, "*") && value.Rules.IsModelSupported(model) {
					modelSet[model] = struct{}{}
				}
			}
			continue
		}
		hasConfiguredModels = true
		for _, model := range requestModels {
			if model = strings.TrimSpace(model); model != "" && !strings.Contains(model, "*") && value.Rules.IsModelSupported(model) {
				modelSet[model] = struct{}{}
			}
		}
	}
	if len(modelSet) == 0 && !hasConfiguredModels {
		return nil
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// matchesRejectionPlatform reports whether a source satisfies the optional
// platform filter.
func matchesRejectionPlatform(value *ModelRejectionSource, platform string) bool {
	return platform == "" || value.Platform == platform
}

// NewGroupModelRejection constructs a group-model error with filtered
// suggestions for a nonempty request and provider slice. Blank requests or an
// empty slice return nil before reading provider rules.
func NewGroupModelRejection(platform, requested string, providers []ModelRejectionSource) error {
	requested = strings.TrimSpace(requested)
	if requested == "" || len(providers) == 0 {
		return nil
	}
	return &GroupModelUnsupportedError{Platform: platform, RequestedModel: requested, AvailableModels: AvailableModelsForRejection(providers, platform)}
}
