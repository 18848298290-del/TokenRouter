package pricing

import (
	"strings"
)

// CatalogQuery uses full model identities to look up prices and attributes.
type CatalogQuery struct {
	Entries    map[string]*CatalogModelPricing
	Candidates func(string) []string
}

// GetModelPricing looks up the requested model through its identity candidates.
func (s *CatalogQuery) GetModelPricing(modelName string) *CatalogModelPricing {
	candidates := s.modelLookupCandidates(modelName)
	return s.LookupModelCatalogEntry(candidates)
}

// GetModelModalities reads modalities from the full model's catalog entry.
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

// LookupModelCatalogEntry tries candidates in order; callers keep the catalog read-only during lookup.
func (s *CatalogQuery) LookupModelCatalogEntry(candidates []string) *CatalogModelPricing {
	for _, candidate := range candidates {
		if pricing := s.Entries[candidate]; pricing != nil {
			return pricing
		}
	}
	return nil
}

func (s *CatalogQuery) modelLookupCandidates(model string) []string {
	if s.Candidates != nil {
		return s.Candidates(model)
	}
	return BuildModelLookupCandidates(model)
}
