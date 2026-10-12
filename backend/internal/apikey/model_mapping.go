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

// A Key model alias redirects one request name to one concrete target before
// group and provider mapping. Validation trims configured names and permits one
// trailing wildcard on the source, while runtime resolution gives exact names
// priority and then chooses the longest matching wildcard. Targets are returned
// once, so an alias pointing at another source is not resolved again.
//
// Model lists expose exact aliases only when their concrete targets appear in
// the supplied available models. Wildcard sources stay out of enumeration
// because they describe a range of names. Existing model order is retained
// after trimming, empty-name removal, and deduplication; eligible aliases are
// appended in lexical order with names already present omitted.

const (
	// Model mapping bounds limit rules examined at request time and fit source
	// and target names into the usage model field limits.
	MaxAPIKeyModelMappingRules = 100 // maximum Key redirect entries
	MaxAPIKeyModelNameRunes    = 100 // maximum model-name runes
)

// ErrInvalidAPIKeyModelMapping identifies malformed or oversized Key model
// redirect rules for service and HTTP validation.
var ErrInvalidAPIKeyModelMapping = infraerrors.BadRequest(
	"INVALID_API_KEY_MODEL_MAPPING",
	"invalid API key model mapping",
)

// NormalizeAPIKeyModelMapping validates rule counts and names and returns a
// fresh map of trimmed strings. Nil input produces an empty map; validation
// stops on an invalid rule, and map iteration determines which competing
// invalid entry is reported. Source matching remains case sensitive, targets
// contain no wildcard, and this check does not establish runtime model
// availability.
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
		// Count Unicode characters so multibyte model names share the storage
		// length allowance with ASCII names rather than losing space to encoding.
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

// ResolveModelMapping trims requestedModel and returns its single redirect
// result with a match flag. Empty input or rules returns the trimmed input with
// false; exact names precede the longest matching trailing-wildcard source, and
// targets are not resolved again.
func ResolveModelMapping(mapping map[string]string, requestedModel string) (string, bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(mapping) == 0 {
		return requestedModel, false
	}
	return modelmap.Resolve(mapping, requestedModel)
}

// ResolveModelMapping resolves one alias using this Key's configured model
// redirects. A nil Key returns the trimmed request name with false, and the
// returned target receives no additional match against this mapping.
func (k *APIKey) ResolveModelMapping(requestedModel string) (string, bool) {
	if k == nil {
		return strings.TrimSpace(requestedModel), false
	}
	return ResolveModelMapping(k.ModelMapping, requestedModel)
}

// CloneModelMapping copies string rules into a newly allocated model mapping.
// Nil and empty inputs both produce a nonnil empty map; subsequent edits to the
// returned map do not affect its source.
func CloneModelMapping(mapping map[string]string) map[string]string {
	cloned := make(map[string]string, len(mapping))
	maps.Copy(cloned, mapping)
	return cloned
}

// AppendAPIKeyModelAliases returns trimmed, deduplicated models followed by
// eligible exact aliases in lexical order. Empty names are omitted, and a nil
// input produces an allocated result.
//
// Alias eligibility is tested against the original available-model input after
// name trimming. A target must already be available; wildcard sources and
// sources already listed as models are omitted. The returned slice is newly
// allocated, so appending aliases does not modify the input slice backing
// storage.
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
	// Enumerate against the supplied target set after preserving its model
	// order; aliases are appended once and cannot make another alias available.
	result = append(result, AvailableAPIKeyModelAliases(models, mapping)...)
	return result
}

// AvailableAPIKeyModelAliases returns sorted exact alias names whose targets
// appear in the supplied models. Model names are trimmed for availability
// checks; configured source and target strings are used as supplied, so callers
// pass normalized rules. Wildcard sources and aliases already listed as models
// are omitted, and nil inputs return a nonnil empty slice.
//
// Eligibility uses the supplied model set throughout enumeration. Returning one
// alias cannot make another alias that targets it eligible in the same result.
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
