package pricing

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Legacy catalog prices can encode context surcharges in field names rather
// than absolute context-tier records. The conversion recognizes positive input
// and output above-price fields, selects the smallest supplied threshold, and
// retains unit factors where a positive base amount cannot establish a ratio.
//
// Cache above-price fields serve diagnostics instead of selecting the surcharge
// rule: reported cache usage follows input-side long-context scaling in
// ordinary calculation. Separate compatibility modality derivation combines
// supplied lists, mode, flags, and image-input pricing while copying source
// lists before appending. Complete model lookup candidates preserve versions
// and provider qualifications, normalizing only known Google protocol-resource
// paths so lookup cannot silently borrow a different model's price.

var (
	// AboveTierPricePattern matches absolute long-context token prices in catalog entries.
	// Service-tier suffixes and cache fields do not contribute to this threshold conversion.
	AboveTierPricePattern = regexp.MustCompile(`^(input|output)_cost_per_token_above_(\d+)k_tokens$`)
	// CacheTierPricePattern matches long-context cache prices for validation diagnostics.
	// Groups capture the base cache field, optional 1h TTL segment, and service-tier suffix.
	CacheTierPricePattern = regexp.MustCompile(`^(cache_(?:creation|read)_input_token_cost)(_above_1hr)?_above_\d+k_tokens((?:_[a-z]+)?)$`)

	// MarketplaceModalityOrder defines recognized compatibility labels and their
	// display order; callers keep the exported slice stable during catalog queries.
	MarketplaceModalityOrder = []string{"text", "image", "audio", "video"}
)

// DeriveLongContextFromAboveTierFields derives one multiplier-based context
// rule from legacy above-price fields. A nil record or existing positive
// long-context rule is unchanged. Malformed JSON and fields without matching
// positive numeric amounts are ignored; callers supply thousand-token
// thresholds whose conversion fits int.
//
// The smallest accepted threshold supplies input and output ratios against
// positive base amounts. Missing or unusable sides retain one, and a rule is
// stored only when at least one factor exceeds one. Cache-specific above-price
// amounts are diagnosed separately because ordinary multiplier-tier billing
// scales cache amounts with the input factor.
func DeriveLongContextFromAboveTierFields(rawEntry json.RawMessage, pricing *CatalogModelPricing) {
	if pricing == nil ||
		pricing.LongContextInputTokenThreshold > 0 ||
		pricing.LongContextInputCostMultiplier > 0 ||
		pricing.LongContextOutputCostMultiplier > 0 {
		return
	}
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return
	}
	var fields map[string]any
	// Malformed JSON cannot supply usable derived fields; the ordinary catalog
	// decoder owns the entry rejection before publication.
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return
	}
	// tierPrices contains the accepted input and output above-price amounts for
	// one threshold.
	type tierPrices struct {
		input, // accepted input above-price amount
		output float64 // accepted output above-price amount
	}
	tiers := make(map[int]*tierPrices)
	for key, value := range fields {
		match := AboveTierPricePattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		price, ok := value.(float64)
		if !ok || price <= 0 {
			continue
		}
		// Only positive machine-sized thousand counts can identify a legacy context
		// threshold; malformed or oversized labels are skipped from automatic rule
		// derivation.
		thousands, err := strconv.Atoi(match[2])
		if err != nil || thousands <= 0 {
			continue
		}
		// Convert thousand-token labels into the context comparison's token unit.
		threshold := thousands * 1000
		tier := tiers[threshold]
		if tier == nil {
			tier = &tierPrices{}
			tiers[threshold] = tier
		}
		if match[1] == "input" {
			tier.input = price
		} else {
			tier.output = price
		}
	}
	if len(tiers) == 0 {
		return
	}
	threshold := 0
	for candidate := range tiers {
		if threshold == 0 || candidate < threshold {
			threshold = candidate
		}
	}
	tier := tiers[threshold]
	inputMultiplier, outputMultiplier := 1.0, 1.0
	if tier.input > 0 && pricing.InputCostPerToken > 0 {
		inputMultiplier = tier.input / pricing.InputCostPerToken
	}
	if tier.output > 0 && pricing.OutputCostPerToken > 0 {
		outputMultiplier = tier.output / pricing.OutputCostPerToken
	}
	// A multiplier rule must increase at least one text bucket; unit or discounted
	// ratios leave existing base pricing selected.
	if inputMultiplier <= 1 && outputMultiplier <= 1 {
		return
	}
	pricing.LongContextInputTokenThreshold = threshold
	pricing.LongContextInputCostMultiplier = inputMultiplier
	pricing.LongContextOutputCostMultiplier = outputMultiplier
}

// IsLopsidedLongContextLadder reports a positive context threshold with exactly
// one increasing text factor. Nil pricing or a nonpositive threshold returns
// false. The result identifies asymmetric input/output surcharges for loader
// diagnostics.
func IsLopsidedLongContextLadder(pricing *CatalogModelPricing) bool {
	if pricing == nil || pricing.LongContextInputTokenThreshold <= 0 {
		return false
	}
	return (pricing.LongContextInputCostMultiplier > 1) != (pricing.LongContextOutputCostMultiplier > 1)
}

// OrphanCacheTierFields returns sorted positive cache above-price keys with no
// eligible positive base in the same raw entry. Matching TTL and service-tier
// suffixes are checked first, followed by bases omitting one suffix or both.
// Any one positive base suppresses the warning; this diagnostic does not select
// a cache rate for calculation.
//
// Malformed JSON returns no diagnostics because ordinary catalog decoding owns
// candidate rejection. Nonmatching fields, nonnumeric values, and nonpositive
// amounts do not establish an orphan warning.
func OrphanCacheTierFields(rawEntry json.RawMessage) []string {
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return nil
	}
	var fields map[string]any
	// Malformed JSON provides no reliable warning keys; ordinary catalog decoding
	// owns rejection while this optional diagnostic returns an empty result.
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return nil
	}
	positive := func(key string) bool {
		price, ok := fields[key].(float64)
		return ok && price > 0
	}
	var orphans []string
	for key := range fields {
		match := CacheTierPricePattern.FindStringSubmatch(key)
		if match == nil || !positive(key) {
			continue
		}
		stem, hourly, tier := match[1], match[2], match[3]
		if positive(stem+hourly+tier) || positive(stem+hourly) || positive(stem+tier) || positive(stem) {
			continue
		}
		orphans = append(orphans, key)
	}
	sort.Strings(orphans)
	return orphans
}

// SanitizeModalities returns recognized unique modality labels in
// MarketplaceModalityOrder. Supplied labels are trimmed and case-folded;
// unsupported values are dropped. The result is newly allocated, and callers
// keep the exported order slice stable during reads.
func SanitizeModalities(values []string) []string {
	present := make(map[string]bool, len(values))
	for _, value := range values {
		present[strings.ToLower(strings.TrimSpace(value))] = true
	}
	out := make([]string, 0, len(MarketplaceModalityOrder))
	for _, modality := range MarketplaceModalityOrder {
		if present[modality] {
			out = append(out, modality)
		}
	}
	return out
}

// DeriveModalities returns compatibility input and output labels from a catalog
// price record. Nil input returns two nil lists. Supplied nonempty lists are
// the starting point, while missing lists receive mode defaults; legacy support
// flags and positive image-input pricing add their corresponding modalities.
//
// Both source slices are copied before additions because published entries may
// be read concurrently. Sanitization deduplicates recognized labels in the
// display order. If either resulting side has no recognized labels, both
// results are nil; this compatibility derivation is distinct from
// modelcatalog's authoritative attribute lookup.
func DeriveModalities(p *CatalogModelPricing) ([]string, []string) {
	if p == nil {
		return nil, nil
	}

	// Appending inferred labels must not reuse published slice capacity, because
	// simultaneous compatibility queries share the source entry's backing arrays.
	input := append([]string{}, p.SupportedModalities...)
	output := append([]string{}, p.SupportedOutputModalities...)
	if len(input) == 0 || len(output) == 0 {
		modeInput, modeOutput := ModalitiesFromMode(p.Mode)
		if len(input) == 0 {
			input = modeInput
		}
		if len(output) == 0 {
			output = modeOutput
		}
	}
	if p.SupportsVision {
		input = append(input, "image")
	}
	if p.SupportsAudioInput {
		input = append(input, "audio")
	}
	if p.SupportsAudioOutput {
		output = append(output, "audio")
	}
	if p.SupportsVideoInput {
		input = append(input, "video")
	}
	if p.InputCostPerImageToken > 0 {
		input = append(input, "image")
	}

	in := SanitizeModalities(input)
	out := SanitizeModalities(output)
	if len(in) == 0 || len(out) == 0 {
		return nil, nil
	}
	return in, out
}

// ModalitiesFromMode returns compatibility modality defaults for the normalized
// legacy mode. Image generation, transcription, speech, and Realtime have their
// listed input/output units. Other modes use text on both sides, with fresh
// slices for every result.
func ModalitiesFromMode(mode string) ([]string, []string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "image_generation":
		return []string{"text"}, []string{"image"}
	case "audio_transcription":
		return []string{"audio"}, []string{"text"}
	case "audio_speech":
		return []string{"text"}, []string{"audio"}
	case "realtime":
		return []string{"text", "audio"}, []string{"text", "audio"}
	default:
		// Unknown legacy modes still need compatibility input/output labels; the
		// default text pair supports those price consumers while attributes remain
		// separately resolved.
		return []string{"text"}, []string{"text"}
	}
}

// BuildModelLookupCandidates returns the complete-identity candidates used by
// pricing and compatibility metadata lookup. Candidates normalize case and
// known Google resource paths. Empty input returns nil; versions, effort
// suffixes, and provider qualifiers retain their identity.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_metadata_lookup
func BuildModelLookupCandidates(model string) []string {
	return BuildModelIdentityCandidates(model)
}

// BuildModelIdentityCandidates returns complete identity keys in lookup order
// after trimming whitespace and folding case. Empty input returns nil. When
// resource normalization changes the text, the complete normalized input key
// comes first and the shortened resource key follows; unchanged names yield
// one key.
//
// Keeping both keys lets an entry stored under the full resource name win before
// lookup uses the shorter name. Date and effort suffixes remain part of either
// key, and ordinary provider qualifiers stay attached to the model text.
func BuildModelIdentityCandidates(model string) []string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	normalized := NormalizeModelNameForPricing(model)
	if normalized == model {
		return []string{model}
	}
	return []string{model, normalized}
}

// NormalizeModelNameForPricing returns model text after removing recognized
// Google protocol resource segments and leading slashes. It trims surrounding
// whitespace, retains case, and removes models/ or publishers/google/models/
// prefixes. A longer path containing /publishers/google/models/ is reduced to
// the suffix after its final occurrence.
//
// Version and effort suffixes remain in the returned text, as do ordinary
// provider qualifiers unless a recognized Google segment occurs. Full-identity
// candidate construction retains the supplied path before this shortened key,
// so an entry configured under that path can take precedence.
func NormalizeModelNameForPricing(model string) string {
	// A resource name can use a short models/ prefix or a longer Google publisher
	// path. Removing those path segments lets catalog keys name the same final
	// model text.
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	return model
}
