package apikey

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

const (
	// MaxAPIKeyModelMappingRules bounds snapshot size and request-time matching work.
	MaxAPIKeyModelMappingRules = 100
	// MaxAPIKeyModelNameRunes matches the usage_logs model field length.
	MaxAPIKeyModelNameRunes = 100
)

var ErrInvalidAPIKeyModelMapping = infraerrors.BadRequest(
	"INVALID_API_KEY_MODEL_MAPPING",
	"invalid API key model mapping",
)

// NormalizeAPIKeyModelMapping validates rules and returns an independent map with trimmed names.
func NormalizeAPIKeyModelMapping(mapping map[string]string) (map[string]string, error) {
	if len(mapping) > MaxAPIKeyModelMappingRules {
		return nil, fmt.Errorf("%w: at most %d rules are allowed", ErrInvalidAPIKeyModelMapping, MaxAPIKeyModelMappingRules)
	}

	normalized := make(map[string]string, len(mapping))
	for rawSource, rawTarget := range mapping {
		source := strings.TrimSpace(rawSource)
		target := strings.TrimSpace(rawTarget)
		if source == "" || target == "" {
			return nil, fmt.Errorf("%w: source and target are required", ErrInvalidAPIKeyModelMapping)
		}
		if utf8.RuneCountInString(source) > MaxAPIKeyModelNameRunes || utf8.RuneCountInString(target) > MaxAPIKeyModelNameRunes {
			return nil, fmt.Errorf("%w: model names must not exceed %d characters", ErrInvalidAPIKeyModelMapping, MaxAPIKeyModelNameRunes)
		}
		if strings.Count(source, "*") > 1 || (strings.Contains(source, "*") && !strings.HasSuffix(source, "*")) {
			return nil, fmt.Errorf("%w: source wildcard is only allowed once at the end", ErrInvalidAPIKeyModelMapping)
		}
		if strings.Contains(target, "*") {
			return nil, fmt.Errorf("%w: target wildcard is not allowed", ErrInvalidAPIKeyModelMapping)
		}
		if source == target {
			return nil, fmt.Errorf("%w: source and target must differ", ErrInvalidAPIKeyModelMapping)
		}
		if _, exists := normalized[source]; exists {
			return nil, fmt.Errorf("%w: duplicate source after trimming", ErrInvalidAPIKeyModelMapping)
		}
		normalized[source] = target
	}
	return normalized, nil
}

// ResolveModelMapping applies one exact or longest-prefix wildcard match.
func ResolveModelMapping(mapping map[string]string, requestedModel string) (string, bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(mapping) == 0 {
		return requestedModel, false
	}
	return modelmap.Resolve(mapping, requestedModel)
}

// ResolveModelMapping returns a single redirect result for this key.
func (k *APIKey) ResolveModelMapping(requestedModel string) (string, bool) {
	if k == nil {
		return strings.TrimSpace(requestedModel), false
	}
	return ResolveModelMapping(k.ModelMapping, requestedModel)
}

// CloneModelMapping returns an independent map for caching or responses.
func CloneModelMapping(mapping map[string]string) map[string]string {
	cloned := make(map[string]string, len(mapping))
	maps.Copy(cloned, mapping)
	return cloned
}

// AppendAPIKeyModelAliases keeps model order and appends sorted exact aliases whose targets are available.
func AppendAPIKeyModelAliases(models []string, mapping map[string]string) []string {
	seen := make(map[string]struct{}, len(models)+len(mapping))
	result := make([]string, 0, len(models)+len(mapping))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	result = append(result, AvailableAPIKeyModelAliases(models, mapping)...)
	return result
}

// AvailableAPIKeyModelAliases returns sorted exact aliases with available targets, excluding existing model names.
func AvailableAPIKeyModelAliases(models []string, mapping map[string]string) []string {
	availableTargets := make(map[string]struct{}, len(models))
	seen := make(map[string]struct{}, len(models)+len(mapping))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		availableTargets[model] = struct{}{}
		seen[model] = struct{}{}
	}
	aliases := make([]string, 0, len(mapping))
	for source, target := range mapping {
		if strings.Contains(source, "*") {
			continue
		}
		if _, available := availableTargets[target]; !available {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		aliases = append(aliases, source)
	}
	sort.Strings(aliases)
	return aliases
}
