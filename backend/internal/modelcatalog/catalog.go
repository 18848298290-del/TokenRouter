package modelcatalog

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"sort"
	"strings"
)

// Catalog providers can offer the same bare model with different prices and
// capabilities. Qualified keys retain each provider's complete entry, while a
// bare key prefers one identified first party record and otherwise remains
// ambiguous when several records compete. Public model metadata is queryable
// even when its price fields are absent.
//
// An ambiguous bare name can still acquire public attributes when every
// competing entry names the same canonical model. That metadata path leaves
// the pricing ambiguity intact. Parsing validates entries before returning a
// versioned catalog; callers apply supplements before publication and keep
// published catalog maps and shared price values read-only for existing views.

// RemoteURL identifies the complete models.dev catalog endpoint used when
// configuring the application's remote catalog loader.
const RemoteURL = "https://models.dev/catalog.json?type=all"

// offlineData and modelSupplements provide the catalog and official supplement
// inputs shipped with each binary.
var (
	// offlineData contains a validated models.dev snapshot for offline startup.
	//
	//go:embed catalog.json.gz
	offlineData []byte

	// modelSupplements contains the official supplements shipped with the binary.
	//
	//go:embed model_supplements.json
	modelSupplements string
)

// Cost preserves missing prices and the source unit of USD per million tokens.
type Cost struct {
	// Input, Output, CacheRead, and CacheWrite retain nil for missing prices and
	// pointers to zero for declared free usage. Conversion to per-token billing
	// prices belongs to the pricing consumer.
	Input      *float64 `json:"input"`       // input USD per million tokens
	Output     *float64 `json:"output"`      // output USD per million tokens
	CacheRead  *float64 `json:"cache_read"`  // cache read USD per million tokens
	CacheWrite *float64 `json:"cache_write"` // cache write USD per million tokens
}

// CostTier carries an explicit context threshold; consumers must not infer it
// from compatibility field names.
type CostTier struct {
	Cost // context tier token prices
	Tier struct {
		Type string `json:"type"` // context threshold tier
		Size int    `json:"size"` // tokens
	} `json:"tier"` // context price activation threshold
}

// Entry contains attributes and prices. Attributes remain available when
// prices are missing.
type Entry struct {
	Model            string            `json:"model"`                        // complete catalog model identity
	Provider         string            `json:"provider"`                     // catalog service provider identity
	Canonical        string            `json:"canonical_model_id,omitempty"` // declared public model identity
	Source           string            `json:"source"`                       // model entry data source
	Attributes       Attributes        `json:"attributes"`                   // model display and control values
	AttributeSources map[string]string `json:"attribute_sources,omitempty"`  // supplemental attribute source labels
	// Cost, Tiers, and Fast can share pointers and slices with a published
	// catalog entry. Lookup and Rows copy attribute data; consumers that change
	// prices also copy these price values, as Index.Search does.
	Cost  Cost       `json:"-"` // base catalog token prices
	Tiers []CostTier `json:"-"` // ordered context price tiers
	Fast  *Cost      `json:"-"` // optional fast mode prices
	// FirstParty records known endpoint authorship, subject to an explicit
	// Canonical identity. Bare-name selection uses it to prefer an author's record.
	FirstParty       bool `json:"-"`
	originalEndpoint bool // original author endpoint match
}

// Catalog holds a parsed catalog version. Apply supplements before publishing
// it to readers.
type Catalog struct {
	Version string // published content version fingerprint
	// Entries, Providers, Ambiguous, attributeFallbacks, and publicEntries are
	// prepared before publication. Existing views retain these maps, so callers
	// finish supplement mutations before sharing a catalog with readers.
	Entries   map[string]Entry // normalized queryable model identities
	Providers map[string]bool  // known catalog provider qualifiers
	Ambiguous map[string]bool  // ambiguous bare price identities
	// attributeFallbacks holds public attributes when all competing provider
	// records agree on one canonical identity that has public metadata available.
	attributeFallbacks map[string]Attributes
	publicEntries      map[string]Entry // public author model metadata
}

// sourceModel decodes the attributes and source-unit prices of one catalog record.
type sourceModel struct {
	Name      string `json:"name"`               // catalog model display name
	Canonical string `json:"canonical_model_id"` // declared public author identity
	// Reasoning, ReasoningOptions, Interleaved, ToolCall, StructuredOutput,
	// Temperature, and Attachment retain supplied false values and empty choices.
	// Nil means the catalog has no declaration for that capability.
	Reasoning        *bool              `json:"reasoning"`         // model reasoning capability flag
	ReasoningOptions *[]ReasoningOption `json:"reasoning_options"` // configurable reasoning control declarations
	Interleaved      *Interleaved       `json:"interleaved"`       // reasoning output format declaration
	ToolCall         *bool              `json:"tool_call"`         // tool call capability flag
	StructuredOutput *bool              `json:"structured_output"` // structured output capability flag
	Temperature      *bool              `json:"temperature"`       // temperature parameter capability flag
	Attachment       *bool              `json:"attachment"`        // attachment input capability flag
	// Limit contains source token counts. entry converts zero limits to unknown
	// attribute values and rejects negative limits before validating the record.
	Limit struct {
		Context int `json:"context"` // tokens
		Input   int `json:"input"`   // tokens
		Output  int `json:"output"`  // tokens
	} `json:"limit"`
	// Modalities.Input and Modalities.Output retain pointer presence for both
	// declarations.
	// Missing lists become unknown attributes, while explicit empty lists remain
	// restrictions that later supplements cannot populate.
	Modalities struct {
		Input  *[]string `json:"input"`  // declared input modality names
		Output *[]string `json:"output"` // declared output modality names
	} `json:"modalities"`
	Cost struct {
		Cost             // catalog base token prices
		Tiers []CostTier `json:"tiers"` // explicit context price tiers
		// Over200K supplies a compatibility tier at 200,000 tokens when Tiers
		// is empty. Explicit tiers take precedence over this historical field.
		Over200K *Cost `json:"context_over_200k"`
	} `json:"cost"` // source catalog pricing object
	// Experimental carries optional mode pricing as raw JSON. entry reads Fast
	// prices from object values and leaves other representations unconsumed.
	Experimental json.RawMessage `json:"experimental"`
}

// Supplements returns a fresh byte slice containing official supplement JSON.
// Catalog loaders validate this body and merge configured custom supplements
// before publishing the resulting attribute and pricing version.
func Supplements() []byte {
	return []byte(modelSupplements)
}

// Offline decompresses the bundled models.dev catalog into fresh bytes.
// Decompression and read failures are returned to the caller, which parses the
// body and applies pricing and attribute supplements before publication.
func Offline() ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(offlineData))
	if err != nil {
		return nil, err
	}
	// Close the decompressor after ReadAll finishes; its lifetime belongs to this
	// load. ReadAll's result determines whether the returned catalog body is
	// usable.
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Parse validates catalog records and constructs qualified entries, preferred
// bare identities, and public metadata lookups. The provider collection is
// required, and malformed public canonical IDs, attributes, prices, or context
// tiers reject the candidate catalog before it is returned.
//
// Bare-name selection prefers a unique first party entry, with the author's
// own endpoint preferred among those entries. Multiple remaining candidates
// produce a pricing ambiguity instead of selecting by map iteration order.
// Public metadata can resolve attributes for that bare name when all provider
// records agree on one canonical identity. Callers apply supplements to the
// returned mutable catalog before sharing its maps through views or indexes.
func Parse(body []byte) (*Catalog, error) {
	var source struct {
		Providers map[string]struct {
			Models map[string]sourceModel `json:"models"` // provider model identity records
		} `json:"providers"` // catalog provider model collections
		Models map[string]sourceModel `json:"models"` // public canonical model metadata
	}
	if err := json.Unmarshal(body, &source); err != nil {
		return nil, err
	}
	if len(source.Providers) == 0 {
		return nil, fmt.Errorf("expected models.dev catalog with providers; legacy pricing JSON is only supported as a local supplement or override")
	}
	hash := sha256.Sum256(body)
	catalog := &Catalog{Version: hex.EncodeToString(hash[:]), Entries: map[string]Entry{}, Providers: map[string]bool{}, Ambiguous: map[string]bool{}}
	bare := map[string][]Entry{}
	for id := range source.Models {
		lab, model, ok := strings.Cut(id, "/")
		if !ok || lab == "" || model == "" {
			return nil, fmt.Errorf("invalid canonical model ID: %s", id)
		}
	}
	for provider, models := range source.Providers {
		catalog.Providers[provider] = true
		lab, knownOrigin := firstPartyProviderLab(provider)
		for id, model := range models.Models {
			entry, err := model.entry(id, provider)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", provider, id, err)
			}
			_, canonicalExists := source.Models[lab+"/"+id]
			entry.FirstParty = canonicalExists || knownOrigin
			// An author's endpoint can also host another author's model. Prefer
			// a supplied canonical identity over endpoint recognition when
			// deciding bare-name ownership.
			if model.Canonical != "" {
				entry.FirstParty = strings.HasPrefix(model.Canonical, lab+"/")
			}
			entry.originalEndpoint = entry.FirstParty && provider == lab
			if entry.Canonical == "" && canonicalExists {
				entry.Canonical = lab + "/" + id
			}
			catalog.Entries[normalize(provider+"/"+id)] = entry
			bare[normalize(id)] = append(bare[normalize(id)], entry)
		}
	}
	for id, candidates := range bare {
		if _, exact := catalog.Entries[id]; exact {
			continue
		}
		var preferred []Entry
		for _, entry := range candidates {
			if entry.FirstParty {
				preferred = append(preferred, entry)
			}
		}
		var original []Entry
		for _, entry := range preferred {
			if entry.originalEndpoint {
				original = append(original, entry)
			}
		}
		// Prefer original author endpoints within the first party candidates for
		// this identity. The remaining candidate count still decides whether
		// bare lookup is ambiguous.
		if len(original) > 0 {
			preferred = original
		}
		if len(preferred) == 1 {
			catalog.Entries[id] = preferred[0]
		} else if len(preferred) == 0 && len(candidates) == 1 {
			catalog.Entries[id] = candidates[0]
		} else {
			catalog.Ambiguous[id] = true
		}
	}
	publicAttributes := make(map[string]Attributes, len(source.Models))
	catalog.publicEntries = make(map[string]Entry, len(source.Models))
	// Publish public metadata even when provider entries have no unique bare
	// price. Attribute lookup can then describe a model with its author's
	// declaration.
	for id, model := range source.Models {
		entry, err := model.entry(id, strings.SplitN(id, "/", 2)[0])
		if err != nil {
			return nil, err
		}
		publicAttributes[normalize(id)] = entry.Attributes
		catalog.publicEntries[normalize(id)] = entry
		if _, exists := catalog.Entries[normalize(id)]; !exists {
			catalog.Entries[normalize(id)] = entry
		}
		bareID := strings.SplitN(id, "/", 2)[1]
		if _, exists := catalog.Entries[normalize(bareID)]; !exists && len(bare[normalize(bareID)]) == 0 {
			catalog.Entries[normalize(bareID)] = entry
		}
	}
	catalog.attributeFallbacks = make(map[string]Attributes)
	// Ambiguous relay prices remain qualified, but unanimous canonical authorship
	// permits public attribute lookup for the bare identity when that metadata
	// exists.
	for id, candidates := range bare {
		if !catalog.Ambiguous[id] || strings.Contains(id, "/") {
			continue
		}
		canonical := normalize(candidates[0].Canonical)
		if canonical == "" {
			continue
		}
		consistent := true
		for _, candidate := range candidates[1:] {
			if normalize(candidate.Canonical) != canonical {
				consistent = false
				break
			}
		}
		if attributes, exists := publicAttributes[canonical]; consistent && exists {
			catalog.attributeFallbacks[id] = attributes
		}
	}
	if len(catalog.Entries) == 0 {
		return nil, fmt.Errorf("empty model catalog")
	}
	return catalog, nil
}

// firstPartyProviderLab returns the recognized author identity for a catalog
// provider. Unrecognized providers return their own name and false, requiring
// canonical model metadata to establish first party authorship.
func firstPartyProviderLab(provider string) (string, bool) {
	switch provider {
	case "openai", "anthropic", "google", "xai", "deepseek", "moonshotai", "mistral", "typesafe":
		return provider, true
	case "moonshotai-cn":
		return "moonshotai", true
	case "zai", "zhipuai":
		return "zhipuai", true
	default:
		return provider, false
	}
}

// FirstPartyAliases returns sorted normalized lookup keys for the same first
// party provider and complete model identity. Nil catalogs, unknown models,
// and relay records return the supplied name as a single candidate.
func (c *Catalog) FirstPartyAliases(model string) []string {
	if c == nil {
		return []string{model}
	}
	entry, found := c.Entries[normalize(model)]
	if !found || !entry.FirstParty {
		return []string{model}
	}
	var aliases []string
	for key, candidate := range c.Entries {
		if candidate.FirstParty && candidate.Provider == entry.Provider && candidate.Model == entry.Model {
			aliases = append(aliases, key)
		}
	}
	sort.Strings(aliases)
	return aliases
}

// entry validates one source model and retains its complete identity and
// source-unit price values. Zero limits become unknown attributes, while
// negative limits and invalid capabilities or prices reject the record.
//
// Explicit context tiers take precedence over the historical context_over_200k
// field, which supplies a 200,000-token tier when explicit tiers are absent.
// Tiers are sorted and validated for positive, distinct thresholds so pricing
// consumers receive ordered values. Optional Fast prices are decoded from an
// experimental object and remain absent when that mode is unspecified.
func (m sourceModel) entry(id, provider string) (Entry, error) {
	if m.Limit.Context < 0 || m.Limit.Input < 0 || m.Limit.Output < 0 {
		return Entry{}, fmt.Errorf("negative model limit")
	}
	// Catalog zero lengths represent unknown limits. Convert them to nil before
	// administrator-style validation, which requires every populated token limit
	// to be positive.
	positive := func(value int) *int {
		if value <= 0 {
			return nil
		}
		return &value
	}
	attrs := Attributes{
		Context:          positive(m.Limit.Context),
		InputLimit:       positive(m.Limit.Input),
		OutputLimit:      positive(m.Limit.Output),
		InputModalities:  m.Modalities.Input,
		OutputModalities: m.Modalities.Output,
		Reasoning:        m.Reasoning,
		ReasoningOptions: m.ReasoningOptions,
		Interleaved:      m.Interleaved,
		ToolCall:         m.ToolCall,
		StructuredOutput: m.StructuredOutput,
		Temperature:      m.Temperature,
		Attachment:       m.Attachment,
	}
	if m.Name != "" {
		attrs.DisplayName = &m.Name
	}
	if err := attrs.Validate(); err != nil {
		return Entry{}, err
	}
	e := Entry{Model: id, Provider: provider, Canonical: m.Canonical, Source: "models.dev", Attributes: attrs, Cost: m.Cost.Cost, Tiers: m.Cost.Tiers}
	// The compatibility field names a fixed context threshold. Materialize that
	// threshold for tier consumers when the catalog has supplied no explicit
	// tier list.
	if len(e.Tiers) == 0 && m.Cost.Over200K != nil {
		tier := CostTier{Cost: *m.Cost.Over200K}
		tier.Tier.Type = "context"
		tier.Tier.Size = 200000
		e.Tiers = []CostTier{tier}
	}
	var experimental struct {
		Modes map[string]struct {
			Cost *Cost `json:"cost"` // optional mode token prices
		} `json:"modes"` // experimental named pricing modes
	}
	if len(m.Experimental) > 0 && m.Experimental[0] == '{' {
		if err := json.Unmarshal(m.Experimental, &experimental); err != nil {
			return Entry{}, err
		}
		e.Fast = experimental.Modes["fast"].Cost
	}
	if err := validateCost(e.Cost); err != nil {
		return Entry{}, err
	}
	if e.Fast != nil {
		if err := validateCost(*e.Fast); err != nil {
			return Entry{}, err
		}
	}
	sort.Slice(e.Tiers, func(i, j int) bool { return e.Tiers[i].Tier.Size < e.Tiers[j].Tier.Size })
	for i, tier := range e.Tiers {
		if tier.Tier.Type != "context" || tier.Tier.Size <= 0 || i > 0 && tier.Tier.Size == e.Tiers[i-1].Tier.Size {
			return Entry{}, fmt.Errorf("invalid context tier")
		}
		if err := validateCost(tier.Cost); err != nil {
			return Entry{}, err
		}
	}
	return e, nil
}

// validateCost rejects negative and nonfinite catalog token price values.
// Nil prices remain unknown and zero prices remain declared free usage, so
// attribute-only entries and free models are accepted.
func validateCost(cost Cost) error {
	for _, value := range []*float64{cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite} {
		if value != nil && (*value < 0 || math.IsInf(*value, 0) || math.IsNaN(*value)) {
			return fmt.Errorf("invalid model price")
		}
	}
	return nil
}

// normalize trims surrounding whitespace and folds case in catalog identity keys.
func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// Lookup searches candidates in order and copies the first entry's attributes
// and attribute source labels. Price pointers and tier slices still belong to
// the catalog; callers copy them before mutation.
func (c *Catalog) Lookup(candidates []string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	for _, candidate := range candidates {
		if entry, ok := c.Entries[normalize(candidate)]; ok {
			entry.Attributes = entry.Attributes.Clone()
			entry.AttributeSources = maps.Clone(entry.AttributeSources)
			return entry, true
		}
	}
	return Entry{}, false
}

// LookupAttributes returns copied attributes from the first resolved candidate,
// then considers public metadata for an unresolved bare identity. Recognized
// provider qualifiers constrain candidate preparation, and an exact entry
// remains authoritative even when some attributes are missing. Nil catalogs
// and missing metadata return false with unknown attributes.
//
// The bare-name path uses the public attributes selected by Parse when competing
// providers agree on one canonical model. Names containing a slash skip this
// path, keeping provider-specific omissions attached to that source. Attribute
// fallback leaves the catalog's pricing ambiguity unchanged, so displaying
// capabilities does not select a relay quote.
func (c *Catalog) LookupAttributes(model string, candidates []string) (Attributes, bool) {
	if entry, found := c.Lookup(c.IdentityCandidates(model, candidates)); found {
		return entry.Attributes, true
	}
	if c == nil || strings.Contains(normalize(model), "/") {
		return Attributes{}, false
	}
	// No exact candidate resolved this bare ID. Read attributeFallbacks,
	// prepared from unanimous canonical identities, while leaving the price
	// entry collection unchanged.
	attributes, found := c.attributeFallbacks[normalize(model)]
	if !found {
		return Attributes{}, false
	}
	return attributes.Clone(), true
}

// Rows returns entries sorted by their normalized lookup keys, including
// qualified names and bare aliases. Attributes and source labels are copied;
// price pointers and tier slices remain shared with the catalog.
func (c *Catalog) Rows() []Entry {
	rows := []Entry{}
	if c == nil {
		return rows
	}
	for key, value := range c.Entries {
		value.Model = key
		value.Attributes = value.Attributes.Clone()
		value.AttributeSources = maps.Clone(value.AttributeSources)
		rows = append(rows, value)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows
}

// IdentityCandidates prepares lookup names while retaining recognized
// provider qualification. A qualified model accepts candidates from the same
// provider and prefixes bare candidates; candidates naming another provider
// are discarded, keeping metadata and price lookup within the requested source.
//
// The normalized input remains the first candidate. For a recognized azure/m
// identity, a bare m candidate becomes azure/m, while relay/m is excluded.
// Unknown qualifiers leave the supplied extra candidates available to Lookup,
// which normalizes them during its search.
func (c *Catalog) IdentityCandidates(model string, candidates []string) []string {
	result := []string{normalize(model)}
	prefix, _, qualified := strings.Cut(normalize(model), "/")
	if c != nil && qualified && c.Providers[prefix] {
		for _, candidate := range candidates {
			candidate = normalize(candidate)
			if strings.HasPrefix(candidate, prefix+"/") {
				result = append(result, candidate)
			} else if !strings.Contains(candidate, "/") {
				result = append(result, prefix+"/"+candidate)
			}
		}
		return result
	}
	return append(result, candidates...)
}

// RequiresExact reports whether ambiguity or a recognized provider qualifier
// requires exact pricing identity. Nil catalogs return false because they
// contain neither provider recognition nor recorded bare-name ambiguities.
func (c *Catalog) RequiresExact(model string) bool {
	if c == nil {
		return false
	}
	prefix, _, qualified := strings.Cut(normalize(model), "/")
	return c.Ambiguous[normalize(model)] || qualified && c.Providers[prefix]
}
