package modelidentity

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Pricing and catalogue consumers use complete model identities, including
// provider qualifiers and version suffixes. The pricing package supplies
// ordered lookup candidates and Google resource normalization, while gateway
// callers receive either the lookup callback or a billing identity value.

// CandidatesFactory returns the complete-identity lookup callback for pricing
// and catalogue consumers.
func CandidatesFactory() func(string) []string {
	return LookupCandidates
}

// LookupCandidates delegates ordered identity lookup to pricing after
// whitespace and case normalization. Empty input returns nil; Google resource
// paths may add a shorter candidate while complete model suffixes remain part
// of each key.
func LookupCandidates(model string) []string {
	return pricing.BuildModelIdentityCandidates(model)
}

// Identity packages the pricing lookup candidates for one model into a billing
// value. Blank model input produces an identity with nil candidates for
// consumers to handle as missing identity.
func Identity(model string) billing.ModelIdentity {
	return billing.ModelIdentity{Candidates: LookupCandidates(model)}
}
