//go:build integration

package provider_test

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Provider administration defaults to name order and can request descending
// global priority. PostgreSQL fixtures deliberately use differing values and
// compare the returned order, reusing ProviderRepoSuite's transaction setup for
// each case.

// TestList_DefaultSortByNameAsc inserts provider names in reverse alphabetical
// order before requesting the default list. The result returns both fixtures in
// ascending name order.
func (s *ProviderRepoSuite) TestList_DefaultSortByNameAsc() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "z-provider"})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a-provider"})

	providers, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(providers, 2)
	s.Require().Equal("a-provider", providers[0].Name)
	s.Require().Equal("z-provider", providers[1].Name)
}

// TestListWithFilters_SortByPriorityDesc inserts providers with priorities ten
// and ninety, then requests descending priority. The result returns both
// fixtures with the priority-ninety provider first.
func (s *ProviderRepoSuite) TestListWithFilters_SortByPriorityDesc() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "low-priority", Priority: 10})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "high-priority", Priority: 90})

	providers, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "priority",
		SortOrder: "desc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(providers, 2)
	s.Require().Equal("high-priority", providers[0].Name)
	s.Require().Equal("low-priority", providers[1].Name)
}
