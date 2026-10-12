package modelidentity

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// CandidatesFactory returns the shared full-model identity lookup function.
func CandidatesFactory() func(string) []string {
	return LookupCandidates
}

// LookupCandidates keeps the complete model name while resolving protocol resource paths.
func LookupCandidates(model string) []string {
	return pricing.BuildModelIdentityCandidates(model)
}

// Identity wraps candidate names for one model lookup.
func Identity(model string) billing.ModelIdentity {
	return billing.ModelIdentity{Candidates: LookupCandidates(model)}
}
