//go:build integration

package provider_test

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Proxy administration can order rows by provider count or expiry, with NULL
// expiry representing no scheduled expiration. PostgreSQL fixtures place
// creation order apart from the requested sort order and check both directions
// together with the positions of rows lacking expiry.

// TestListWithFiltersAndProviderCount_SortByProviderCountDesc creates proxies
// referenced by two providers and one provider. Descending count order places
// the more-used proxy first and reports its count as two.
func (s *ProxyRepoSuite) TestListWithFiltersAndProviderCount_SortByProviderCountDesc() {
	p1 := s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	p2 := s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})
	s.mustInsertProvider("a1", &p1.ID)
	s.mustInsertProvider("a2", &p1.ID)
	s.mustInsertProvider("a3", &p2.ID)

	proxies, _, err := s.repo.ListWithFiltersAndProviderCount(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "provider_count",
		SortOrder: "desc",
	}, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 2)
	s.Require().Equal(p1.ID, proxies[0].ID)
	s.Require().Equal(int64(2), proxies[0].ProviderCount)
	s.Require().Equal(p2.ID, proxies[1].ID)
}

// TestListWithFiltersAndProviderCount_SortByExpiry inserts expired, near,
// distant, and nonexpiring proxies in a different creation order. Ascending
// expiry places NULL last, while descending expiry places it first.
func (s *ProxyRepoSuite) TestListWithFiltersAndProviderCount_SortByExpiry() {
	now := time.Now()
	past := now.Add(-24 * time.Hour)
	soon := now.Add(72 * time.Hour)
	later := now.Add(100 * 24 * time.Hour)

	// Creation order differs from both requested expiry orders, so the ID-based
	// ordering cannot accidentally satisfy these NULL-placement and timestamp comparisons.
	pLater := s.mustCreateProxy(&egress.Proxy{Name: "p-later", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive, ExpiresAt: &later})
	pNever := s.mustCreateProxy(&egress.Proxy{Name: "p-never", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive, ExpiresAt: nil})
	pExpired := s.mustCreateProxy(&egress.Proxy{Name: "p-expired", Protocol: "http", Host: "127.0.0.1", Port: 8082, Status: billing.StatusActive, ExpiresAt: &past})
	pSoon := s.mustCreateProxy(&egress.Proxy{Name: "p-soon", Protocol: "http", Host: "127.0.0.1", Port: 8083, Status: billing.StatusActive, ExpiresAt: &soon})

	// Query ascending order with all four expiry shapes present, separating
	// the no-expiry row from expired, near-term, and distant scheduled expirations.
	asc, _, err := s.repo.ListWithFiltersAndProviderCount(s.ctx, pagination.PaginationParams{
		Page: 1, PageSize: 10, SortBy: "expiry", SortOrder: "asc",
	}, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(asc, 4)
	s.Require().Equal(
		[]int64{pExpired.ID, pSoon.ID, pLater.ID, pNever.ID},
		[]int64{asc[0].ID, asc[1].ID, asc[2].ID, asc[3].ID},
		"asc: 过期→快到期→远期→永不过期(垫底)",
	)

	// Reverse the same fixture ordering so the no-expiry row moves first
	// and the remaining timestamp comparisons prove descending expiry handling.
	desc, _, err := s.repo.ListWithFiltersAndProviderCount(s.ctx, pagination.PaginationParams{
		Page: 1, PageSize: 10, SortBy: "expiry", SortOrder: "desc",
	}, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(desc, 4)
	s.Require().Equal(
		[]int64{pNever.ID, pLater.ID, pSoon.ID, pExpired.ID},
		[]int64{desc[0].ID, desc[1].ID, desc[2].ID, desc[3].ID},
		"desc: 永不过期(置顶)→远期→快到期→过期",
	)
}
