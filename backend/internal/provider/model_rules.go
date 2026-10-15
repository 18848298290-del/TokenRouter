package provider

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// model_rules.go prepares indexed provider model rules for repeated mapping
// and eligibility checks.
// Catalogue and batch queries test many names against the same provider rules.
// Prepared rules copy mapping entries and index the effective whitelist once,
// keeping raw mapping precedence separate from trimmed, case-insensitive
// whitelist membership. Terminal mapping wildcards are sorted by specificity,
// while exact whitelist keys retain Unicode EqualFold equivalence.
//
// Mapping factories and finite platform candidate factories are read during
// preparation. Later checks reuse their captured values, but provider identity
// and platform eligibility still come from a borrowed Record and callbacks that
// may read its live credentials. Callers keep those inputs stable for the query.
// Returned maps and candidate slices are copied so editing a query result does
// not change the prepared mapping or candidate names.

// ModelRulesSnapshot holds captured model configuration and matching indexes
// together with the provider and eligibility callbacks used by a query.
type ModelRulesSnapshot struct {
	// record and rules remain borrowed while configuration indexes are copied.
	// Keep the record and callback state stable because Supports reads their live
	// platform and account eligibility during each query check.
	record *Record            // provider used for eligibility
	rules  ModelPlatformRules // platform callbacks for eligibility
	// mapping and mappingPatterns share case-sensitive source keys. resolve checks
	// exact keys first, then patterns ordered by descending length and lexical ties.
	mapping         map[string]string // captured one-hop mapping targets
	mappingPatterns []string          // sorted terminal wildcard sources
	// exact indexes the trimmed effective whitelist using Unicode SimpleFold keys.
	// whitelistMember constructs the same keys when checking each candidate model.
	exact map[string]struct{} // folded exact whitelist keys
	// qoder is populated only with a supplied Qoder normalizer. Later alias checks
	// invoke that same callback to compare a requested route with captured keys.
	qoder map[string]struct{}
	// prefixes stores lowercased terminal wildcard prefixes from the effective
	// scope. whitelistMember lowercases requested models before checking these prefixes.
	prefixes []string // lowercased terminal wildcard prefixes
	// configured stores sorted candidate spellings captured during preparation.
	// ConfiguredModels returns a copied slice, leaving this collection query-owned.
	configured []string
}

// PrepareModelRules captures mappings, whitelist indexes, and finite configured
// candidates for repeated model checks. A nil record produces a nonnil snapshot
// that rejects admission and has empty configuration. Antigravity default
// selection requires its mapping factory when no usable configured mapping exists.
//
// Each supplied factory is read at most once during preparation. Mapping and
// whitelist collections become detached indexes, while record and eligibility
// callbacks remain shared for subsequent checks. Callers keep those shared
// inputs stable, including any credentials read by callbacks, for the query's
// lifetime. Exact mapping precedence and Unicode whitelist equivalence match
// the per-call helpers rather than introducing a separate admission policy.
func PrepareModelRules(record *Record, defaults ModelMappingDefaults, rules ModelPlatformRules) *ModelRulesSnapshot {
	result := &ModelRulesSnapshot{record: record, rules: rules, exact: map[string]struct{}{}}
	if record == nil {
		return result
	}
	result.mapping = ResolveModelMapping(record, defaults)
	for pattern := range result.mapping {
		if strings.HasSuffix(pattern, "*") {
			result.mappingPatterns = append(result.mappingPatterns, pattern)
		}
	}
	// Match the per-call modelmap ordering: longer terminal patterns win, and
	// lexical ties keep selection independent of the source map's iteration order.
	sort.Slice(result.mappingPatterns, func(i, j int) bool {
		left, right := result.mappingPatterns[i], result.mappingPatterns[j]
		if len(left) != len(right) {
			return len(left) > len(right)
		}
		return left < right
	})
	var platformModels []string
	if defaults.Models != nil {
		platformModels = defaults.Models(record)
		// Spark scope construction also asks for platform models. Reuse this read so
		// its whitelist intersection and candidate enumeration see the same names.
		defaults.Models = func(*Record) []string { return platformModels }
	}
	scope := record.effectiveModelScope(defaults, result.mapping)
	for pattern := range scope {
		pattern = strings.TrimSpace(pattern)
		result.exact[modelWhitelistKey(pattern)] = struct{}{}
		if prefix, wildcard := strings.CutSuffix(pattern, "*"); wildcard {
			result.prefixes = append(result.prefixes, strings.ToLower(prefix))
		}
		if record.Platform == PlatformQoder && rules.NormalizeQoder != nil {
			if result.qoder == nil {
				result.qoder = map[string]struct{}{}
			}
			result.qoder[rules.NormalizeQoder(pattern)] = struct{}{}
		}
	}
	result.configured = configuredRequestModels(result.mapping, scope, platformModels, result.whitelistMember)
	return result
}

// Mapping returns a caller-owned copy of the captured string mapping.
// A nonnil snapshot is required. Nil mappings remain nil, and changes to the
// returned map do not change subsequent snapshot matches.
func (s *ModelRulesSnapshot) Mapping() map[string]string { return maps.Clone(s.mapping) }

// ConfiguredModels returns a copied slice of sorted configured candidate names.
// A nonnil snapshot is required. Nil and empty slice shapes are retained, and
// callers can edit the returned elements independently of the snapshot.
func (s *ModelRulesSnapshot) ConfiguredModels() []string { return slices.Clone(s.configured) }

// ResolveMappedModel returns one captured target and whether its mapping matched.
// A nonnil snapshot is required, and an empty model cannot match. Stored empty
// or identity targets still report a match and are returned as stored.
//
// The raw spelling completes both exact and wildcard matching before a trimmed
// spelling is retried. Thus a raw wildcard can outrank a trimmed exact key.
// Missing rules return the original spelling with false, while matched targets
// are never fed back through the mapping for another hop.
func (s *ModelRulesSnapshot) ResolveMappedModel(model string) (string, bool) {
	if mapped, found := s.resolve(model); found {
		return mapped, true
	}
	// Retry trimming only after raw exact and wildcard checks miss. This retains
	// the supplied spelling's precedence when whitespace affects a configured key.
	if trimmed := strings.TrimSpace(model); trimmed != model {
		if mapped, found := s.resolve(trimmed); found {
			return mapped, true
		}
	}
	return model, false
}

// resolve checks a raw model against exact keys and ordered terminal wildcards.
// A nonnil snapshot is required. Empty input returns false even with an empty
// key; stored targets are returned unchanged with true, including empty targets.
func (s *ModelRulesSnapshot) resolve(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	if mapped, found := s.mapping[model]; found {
		return mapped, true
	}
	for _, pattern := range s.mappingPatterns {
		if strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
			return s.mapping[pattern], true
		}
	}
	return model, false
}

// ForwardMappedModel returns a trimmed captured mapping target for forwarding.
// A nonnil snapshot is required. When the mapping misses or its target is blank,
// the model is returned exactly as supplied for downstream platform handling.
func (s *ModelRulesSnapshot) ForwardMappedModel(model string) string {
	mapped, matched := s.ResolveMappedModel(model)
	// A blank target supplies no outbound model. Retain the requested spelling so
	// forwarding passes the caller's model onward to its platform normalizer.
	if !matched || strings.TrimSpace(mapped) == "" {
		return model
	}
	return strings.TrimSpace(mapped)
}

// Supports reports whether one mapped model passes the captured whitelist and
// current platform eligibility. Nil snapshots and nil records reject the model.
// The captured mapping executes once; supplied callbacks can read the retained
// record, so its platform and callback state must remain stable during use.
func (s *ModelRulesSnapshot) Supports(model string) bool {
	if s == nil || s.record == nil {
		return false
	}
	mapped, _ := s.ResolveMappedModel(model)
	return s.whitelisted(mapped) && s.record.modelPlatformAllows(mapped, s.rules)
}

// FinalModelWhitelisted checks captured scope membership and Qoder site
// eligibility for a model already selected by the caller. Nil snapshots or records
// reject it. This method uses model directly; full OpenAI OAuth eligibility is
// evaluated by Supports when required.
func (s *ModelRulesSnapshot) FinalModelWhitelisted(model string) bool {
	if s == nil || s.record == nil {
		return false
	}
	if s.record.Platform == PlatformQoder && s.rules.QoderCompatible != nil && !s.rules.QoderCompatible(model) {
		return false
	}
	return s.whitelisted(model)
}

// whitelisted reports captured scope membership, including Qoder alias keys.
// A nonnil snapshot is required. When ordinary membership misses and Qoder keys
// were prepared, the retained normalizer checks the trimmed model's route key.
func (s *ModelRulesSnapshot) whitelisted(model string) bool {
	if s.whitelistMember(model) {
		return true
	}
	// Qoder route aliases can differ from their whitelist spelling. Use the same
	// normalizer that built qoder so alias checks compare the captured route keys.
	if s.qoder != nil {
		_, found := s.qoder[s.rules.NormalizeQoder(strings.TrimSpace(model))]
		return found
	}
	return false
}

// whitelistMember checks trimmed models against exact keys and wildcard prefixes.
// A nonnil snapshot is required. Empty indexes reject every model; a captured
// global wildcard has an empty prefix and also accepts an empty model.
func (s *ModelRulesSnapshot) whitelistMember(model string) bool {
	model = strings.TrimSpace(model)
	if _, found := s.exact[modelWhitelistKey(model)]; found {
		return true
	}
	lower := strings.ToLower(model)
	for _, prefix := range s.prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// modelWhitelistKey gives exact whitelist names a common key under Unicode
// EqualFold equivalence. It leaves surrounding whitespace intact, so callers
// trim names before constructing or querying their whitelist indexes.
//
// ASCII capitals use lowercase keys. Other runes traverse their SimpleFold cycle
// to choose one representative, then lowercase an ASCII capital representative.
// This joins such equivalents as K and the Kelvin sign, or the sigma forms,
// without treating general lowercasing as equivalent to EqualFold. Wildcard
// prefixes use their separately documented lowercase comparison.
func modelWhitelistKey(value string) string {
	return strings.Map(func(r rune) rune {
		// ASCII letters can use direct lowercase arithmetic.
		if r < unicode.MaxASCII {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}
		// SimpleFold can link more than an upper/lower pair. Choose a cycle-wide
		// representative so every exact-key spelling in that equivalence class agrees.
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		if minimum >= 'A' && minimum <= 'Z' {
			return minimum + ('a' - 'A')
		}
		return minimum
	}, value)
}

// configuredRequestModels returns sorted, distinct configured candidate names.
// Concrete scope entries are included directly; platform names require allows,
// and mapping sources qualify according to their target's membership. The allows
// callback must be callable whenever supplied platform names or mappings need it.
//
// Every name containing an asterisk is excluded as a concrete mapping endpoint
// or scope name. Nonempty targets can themselves become candidates, while an
// empty target can still admit its concrete source if allows accepts it. Spelling
// and whitespace are retained, including empty concrete sources. Callers apply
// platform and protocol admission after this finite candidate collection.
func configuredRequestModels(mapping map[string]string, scope map[string]struct{}, platformModels []string, allows func(string) bool) []string {
	models := make(map[string]struct{})
	for model := range scope {
		if !strings.Contains(model, "*") {
			models[model] = struct{}{}
		}
	}
	for _, model := range platformModels {
		if allows(model) {
			models[model] = struct{}{}
		}
	}
	for source, target := range mapping {
		if target != "" && !strings.Contains(target, "*") && allows(target) {
			models[target] = struct{}{}
		}
		if !strings.Contains(source, "*") && allows(target) {
			models[source] = struct{}{}
		}
	}
	out := make([]string, 0, len(models))
	for model := range models {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}
