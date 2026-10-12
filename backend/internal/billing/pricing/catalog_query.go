package pricing

import (
	"strings"
)

// Model price lookup uses complete identity candidates in their supplied order.
// Stored catalog entries remain shared read-only objects, and a caller-provided
// candidate generator can constrain the model identities before lookup;
// compatibility modality queries return newly derived lists.

// CatalogQuery uses full model identities to look up prices and attributes.
type CatalogQuery struct {
	// Entries holds shared price pointers keyed for the candidate generator's
	// lookups. Callers synchronize changes and keep selected records read-only
	// during queries.
	Entries map[string]*CatalogModelPricing // shared catalog price map
	// Candidates supplies ordered lookup keys when configured, replacing the
	// default full-identity generator. The callback needs to remain callable for
	// the query lifetime.
	Candidates func(string) []string // optional lookup key generator
}

// GetModelPricing returns the first stored price among complete identity
// candidates. It requires a non-nil query; an absent Candidates callback uses
// the default identity generator. Callers keep entries stable during lookup and
// treat returned shared price pointers as read-only. Misses return nil.
func (s *CatalogQuery) GetModelPricing(modelName string) *CatalogModelPricing {
	candidates := s.modelLookupCandidates(modelName)
	return s.LookupModelCatalogEntry(candidates)
}

// GetModelModalities derives compatibility input and output lists from the
// matching price entry. Nil queries and blank model names return nil lists.
// Returned lists are newly allocated by modality derivation; callers keep the
// catalog stable during lookup.
func (s *CatalogQuery) GetModelModalities(modelName string) ([]string, []string) {
	if s == nil {
		return nil, nil
	}

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	if modelLower == "" {
		return nil, nil
	}

	lookupCandidates := s.modelLookupCandidates(modelLower)
	return DeriveModalities(s.LookupModelCatalogEntry(lookupCandidates))
}

// LookupModelCatalogEntry returns the first non-nil stored price for the
// supplied keys. It requires a non-nil query and reads candidates exactly as
// supplied. Callers synchronize map mutation and treat the shared returned
// price as read-only.
func (s *CatalogQuery) LookupModelCatalogEntry(candidates []string) *CatalogModelPricing {
	for _, candidate := range candidates {
		if pricing := s.Entries[candidate]; pricing != nil {
			return pricing
		}
	}
	return nil
}

// modelLookupCandidates uses the configured generator or the complete-identity
// default. The query must be non-nil, and a configured callback returns the
// desired ordered keys as supplied. Without it, BuildModelLookupCandidates
// normalizes known resource paths.
func (s *CatalogQuery) modelLookupCandidates(model string) []string {
	if s.Candidates != nil {
		return s.Candidates(model)
	}
	return BuildModelLookupCandidates(model)
}
