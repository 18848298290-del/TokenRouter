package billing

import (
	"context"
	"strings"

	purepricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Configured cards are searched using the requested full model name before
// same-model protocol resource candidates. That ordering lets a manual exact
// or wildcard card retain precedence, including a free amount, while catalog
// lookup supplies inherited buckets only when the selected card needs them.
// Unknown models remain unpriced when neither source establishes usable
// amounts.
//
// Billing settings are read independently of model-card matches, so a
// configuration with only settings can still affect its groups. Resolve passes
// the chosen card, base source and long-context enablement to the pure pricing
// rules, while identity candidate construction stays with the injected model
// resolver.

// Pricing source labels distinguish a resolved catalog base from unavailable
// model amounts so display and calculation can retain missing-price state.
const (
	PricingSourceCatalog  = purepricing.PricingSourceCatalog  // catalog base pricing is available
	PricingSourceUnpriced = purepricing.PricingSourceUnpriced // usable base pricing is absent
)

// ResolvedPricing aliases the selected price card and catalog base calculation
// data.
type ResolvedPricing = purepricing.ResolvedPricing

// PricingInput identifies the full billing model and optional shared
// configuration group.
type PricingInput struct {
	Model string // full requested model identity
	// GroupID selects shared pricing configuration during Resolve. Nil skips that
	// lookup; a non-nil value is passed to the reader as supplied.
	GroupID *int64 // shared price configuration group
}

// ConfigPrices reads an effective configured card for a group and model name.
type ConfigPrices interface {
	// GetEffectiveConfigModelPricing returns the selected configured card or nil
	// when no usable configuration matches. The source owns exact/wildcard
	// selection and returned-card ownership for this group and model.
	GetEffectiveConfigModelPricing(context.Context, int64, string) *ModelPricingEntry
}

// ModelIdentity carries ordered lookup candidates representing the same full
// model identity.
type ModelIdentity struct {
	// Candidates is supplied in lookup order by the identity resolver.
	// LookupPricingForModel visits these names after the original request,
	// retaining the original card's precedence.
	Candidates []string // ordered same-model lookup names
}

// ModelCandidates derives same-model protocol resource candidates for
// configured price lookup.
type ModelCandidates func(string) ModelIdentity

// PriceResolver selects configured cards, catalog bases and effective billing
// settings for requests.
type PriceResolver struct {
	// pricingConfigs may be nil to use catalog pricing and default billing
	// settings. Optional settings capability on this reader is queried
	// independently of card presence.
	pricingConfigs ConfigPrices // effective configured card reader
	// calculator is used when a selected card needs catalog base amounts. The
	// caller supplies a usable instance for those resolution paths.
	calculator *Calculator // catalog calculation rate reader
	// lookup may be nil to search only the requested name. Supplied candidates
	// represent the same model; cross-model alias pricing is outside this lookup
	// rule.
	lookup  ModelCandidates     // same-model identity candidate resolver
	observe func(string, error) // unavailable base price observer
	// providerStats is the first optional source supplied at construction.
	// Provider cost calculation consults it separately from the user-facing card
	// resolution described here.
	providerStats ProviderStatsSource // provider statistical cost source
}

// NewPriceResolver binds configuration reads, the calculator and optional
// identity/observation callbacks. Nil observation becomes a no-op, only the
// first supplied statistics source is retained, and base-price resolution
// requires a usable calculator.
func NewPriceResolver(pricingConfigs ConfigPrices, calculator *Calculator, lookup ModelCandidates, observe func(string, error), stats ...ProviderStatsSource) *PriceResolver {
	if observe == nil {
		observe = func(string, error) {}
	}
	var providerStats ProviderStatsSource
	if len(stats) > 0 {
		providerStats = stats[0]
	}
	return &PriceResolver{pricingConfigs: pricingConfigs, calculator: calculator, lookup: lookup, observe: observe, providerStats: providerStats}
}

// Resolve returns the selected card, usable catalog base and long-context
// setting for the request. A nil GroupID skips configured lookup; a supplied
// group first tries the requested model and then same-identity candidates. The
// configuration reader determines exact and wildcard matching.
//
// Catalog lookup runs when the selected card needs inherited base amounts,
// allowing standalone cards to price uncataloged models. Effective billing
// settings are read separately even when no model card matches. The pure
// resolver combines these inputs and retains an unavailable base as unpriced
// data.
// @project-doc docs/domains/routing_and_billing.md#group_model_pricing
func (r *PriceResolver) Resolve(ctx context.Context, input PricingInput) *ResolvedPricing {
	var configPricing *ModelPricingEntry
	if input.GroupID != nil && r.pricingConfigs != nil {
		configPricing = r.LookupConfigPricingNormalized(ctx, *input.GroupID, input.Model)
	}
	var base *ModelPricing
	source := PricingSourceUnpriced
	if purepricing.PriceCardNeedsBase(configPricing) {
		base, source = r.ResolveBasePricing(input.Model)
	}
	settings := r.BillingSettings(ctx, input.GroupID)
	return purepricing.ResolvePriceCards(configPricing, base, source, settings.LongContextPricingEnabled)
}

// ResolveBasePricing reads catalog calculation rates for the full supplied
// model. Calculator errors are observed and converted to nil with
// PricingSourceUnpriced, while successful lookup returns PricingSourceCatalog
// for the pure card resolver.
func (r *PriceResolver) ResolveBasePricing(model string) (*ModelPricing, string) {
	pricing, err := r.calculator.GetModelPricing(model)
	if err != nil {
		// A failed base lookup leaves the model unpriced for subsequent card
		// resolution; report its diagnostic while retaining the request's configured
		// selection.
		r.observe(model, err)
		return nil, PricingSourceUnpriced
	}
	return pricing, PricingSourceCatalog
}

// LookupConfigPricingNormalized queries requested-name configuration before
// optional same-identity candidates. Nil resolver or configuration source
// yields nil; lookup matching is owned by GetEffectiveConfigModelPricing, so a
// manual full-name card can price an uncataloged model.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_metadata_lookup
func (r *PriceResolver) LookupConfigPricingNormalized(ctx context.Context, groupID int64, model string) *ModelPricingEntry {
	if r == nil || r.pricingConfigs == nil {
		return nil
	}
	return LookupPricingForModel(model, func(candidate string) *ModelPricingEntry {
		return r.pricingConfigs.GetEffectiveConfigModelPricing(ctx, groupID, candidate)
	}, r.lookup)
}

// LookupPricingForModel calls the required lookup with the supplied model
// before reading identity candidates. A first match returns immediately,
// including configured free amounts; absent identities or exhausted candidates
// yield nil.
//
// The identity callback supplies candidate ordering and their relationship to
// the requested model. Candidates equal to its trimmed spelling ignoring case
// are skipped after the original lookup, so the requested-name result retains
// precedence over protocol-resource alternatives for that same identity.
func LookupPricingForModel(model string, lookup func(string) *ModelPricingEntry, identities ModelCandidates) *ModelPricingEntry {
	if pricing := lookup(model); pricing != nil {
		return pricing
	}
	// The original requested-name lookup has missed; visit only the identity
	// resolver's alternatives, keeping manual requested-name cards ahead of those
	// resource spellings.
	projection := ModelIdentity{}
	if identities != nil {
		projection = identities(model)
	}
	candidates := projection.Candidates
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, strings.TrimSpace(model)) {
			continue
		}
		if pricing := lookup(candidate); pricing != nil {
			return pricing
		}
	}
	return nil
}

// BillingSettings returns a cloned effective settings record when the
// resolver, group ID and optional settings reader are available. Otherwise it
// returns unified defaults; model-card selection does not gate this
// configuration-level lookup.
func (r *PriceResolver) BillingSettings(ctx context.Context, groupID *int64) purepricing.BillingSettings {
	if r != nil && groupID != nil {
		if source, ok := r.pricingConfigs.(interface {
			// GetEffectiveBillingSettings returns the group's configuration-level
			// settings, including when no model card matched. BillingSettings clones the
			// returned value before handing it to pricing callers.
			GetEffectiveBillingSettings(context.Context, int64) purepricing.BillingSettings
		}); ok {
			return source.GetEffectiveBillingSettings(ctx, *groupID).Clone()
		}
	}
	// An absent group or optional settings reader uses unified defaults, so
	// configuration-level behavior remains defined even when no card source is
	// available.
	return purepricing.DefaultBillingSettings()
}
