//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type ProxyRepoSuite struct {
	suite.Suite
	ctx  context.Context
	tx   *dbent.Tx
	repo *postgres.ProxyStore
}

func (s *ProxyRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.tx = tx
	s.repo = newProxyStoreContract(tx.Client(), tx)
}

func TestProxyRepoSuite(t *testing.T) {
	suite.Run(t, new(ProxyRepoSuite))
}

func (s *ProxyRepoSuite) TestCreate() {
	proxy := &egress.Proxy{
		Name:     "test-create",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   billing.StatusActive,
	}

	err := s.repo.Create(s.ctx, proxy)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(proxy.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("test-create", got.Name)
}

func (s *ProxyRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

func (s *ProxyRepoSuite) TestUpdate() {
	proxy := &egress.Proxy{
		Name:     "original",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   billing.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))

	proxy.Name = "updated"
	err := s.repo.Update(s.ctx, proxy)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", got.Name)
}

func (s *ProxyRepoSuite) TestDelete() {
	proxy := &egress.Proxy{
		Name:     "to-delete",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   billing.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))

	err := s.repo.Delete(s.ctx, proxy.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().Error(err, "expected error after delete")
}

func (s *ProxyRepoSuite) TestList() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(proxies, 2)
	s.Require().Equal(int64(2), page.Total)
}

func (s *ProxyRepoSuite) TestListWithFilters_Protocol() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "socks5", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "socks5", "", "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal("socks5", proxies[0].Protocol)
}

func (s *ProxyRepoSuite) TestListWithFilters_Status() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusDisabled})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", billing.StatusDisabled, "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal(billing.StatusDisabled, proxies[0].Status)
}

func (s *ProxyRepoSuite) TestListWithFilters_Search() {
	s.mustCreateProxy(&egress.Proxy{Name: "production-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "dev-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "prod")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Contains(proxies[0].Name, "production")
}

func (s *ProxyRepoSuite) TestListActive() {
	s.mustCreateProxy(&egress.Proxy{Name: "active1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "inactive1", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusDisabled})

	proxies, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(proxies, 1)
	s.Require().Equal("active1", proxies[0].Name)
}

func (s *ProxyRepoSuite) TestExistsByHostPortAuth() {
	s.mustCreateProxy(&egress.Proxy{
		Name:     "p1",
		Protocol: "http",
		Host:     "1.2.3.4",
		Port:     8080,
		Username: "user",
		Password: "pass",
		Status:   billing.StatusActive,
	})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "user", "pass")
	s.Require().NoError(err, "ExistsByHostPortAuth")
	s.Require().True(exists)

	notExists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "wrong", "creds")
	s.Require().NoError(err)
	s.Require().False(notExists)
}

func (s *ProxyRepoSuite) TestExistsByHostPortAuth_NoAuth() {
	s.mustCreateProxy(&egress.Proxy{
		Name:     "p-noauth",
		Protocol: "http",
		Host:     "5.6.7.8",
		Port:     8081,
		Username: "",
		Password: "",
		Status:   billing.StatusActive,
	})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "5.6.7.8", 8081, "", "")
	s.Require().NoError(err)
	s.Require().True(exists)
}

func (s *ProxyRepoSuite) TestCountProvidersByProxyID() {
	proxy := s.mustCreateProxy(&egress.Proxy{Name: "p-count", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustInsertProvider("a1", &proxy.ID)
	s.mustInsertProvider("a2", &proxy.ID)
	s.mustInsertProvider("a3", nil)

	count, err := s.repo.CountProvidersByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err, "CountProvidersByProxyID")
	s.Require().Equal(int64(2), count)
}

func (s *ProxyRepoSuite) TestCountProvidersByProxyID_Zero() {
	proxy := s.mustCreateProxy(&egress.Proxy{Name: "p-zero", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})

	count, err := s.repo.CountProvidersByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Zero(count)
}

func (s *ProxyRepoSuite) TestGetProviderCountsForProxies() {
	p1 := s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	p2 := s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	s.mustInsertProvider("a1", &p1.ID)
	s.mustInsertProvider("a2", &p1.ID)
	s.mustInsertProvider("a3", &p2.ID)

	counts, err := s.repo.GetProviderCountsForProxies(s.ctx)
	s.Require().NoError(err, "GetProviderCountsForProxies")
	s.Require().Equal(int64(2), counts[p1.ID])
	s.Require().Equal(int64(1), counts[p2.ID])
}

func (s *ProxyRepoSuite) TestGetProviderCountsForProxies_Empty() {
	counts, err := s.repo.GetProviderCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Empty(counts)
}

func (s *ProxyRepoSuite) TestListActiveWithProviderCount() {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	p1 := s.mustCreateProxyWithTimes("p1", billing.StatusActive, base.Add(-1*time.Hour))
	p2 := s.mustCreateProxyWithTimes("p2", billing.StatusActive, base)
	s.mustCreateProxyWithTimes("p3-inactive", billing.StatusDisabled, base.Add(1*time.Hour))

	s.mustInsertProvider("a1", &p1.ID)
	s.mustInsertProvider("a2", &p1.ID)
	s.mustInsertProvider("a3", &p2.ID)

	withCounts, err := s.repo.ListActiveWithProviderCount(s.ctx)
	s.Require().NoError(err, "ListActiveWithProviderCount")
	s.Require().Len(withCounts, 2, "expected 2 active proxies")

	// Newest proxies come first (created_at DESC).
	s.Require().Equal(p2.ID, withCounts[0].ID)
	s.Require().Equal(int64(1), withCounts[0].ProviderCount)
	s.Require().Equal(p1.ID, withCounts[1].ID)
	s.Require().Equal(int64(2), withCounts[1].ProviderCount)
}

func (s *ProxyRepoSuite) TestExistsByHostPortAuth_And_ProviderCountAggregates() {
	p1 := s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "1.2.3.4", Port: 8080, Username: "u", Password: "p", Status: billing.StatusActive})
	p2 := s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "5.6.7.8", Port: 8081, Username: "", Password: "", Status: billing.StatusActive})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "u", "p")
	s.Require().NoError(err, "ExistsByHostPortAuth")
	s.Require().True(exists, "expected proxy to exist")

	s.mustInsertProvider("a1", &p1.ID)
	s.mustInsertProvider("a2", &p1.ID)
	s.mustInsertProvider("a3", &p2.ID)

	count1, err := s.repo.CountProvidersByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err, "CountProvidersByProxyID")
	s.Require().Equal(int64(2), count1, "expected 2 providers for p1")

	counts, err := s.repo.GetProviderCountsForProxies(s.ctx)
	s.Require().NoError(err, "GetProviderCountsForProxies")
	s.Require().Equal(int64(2), counts[p1.ID])
	s.Require().Equal(int64(1), counts[p2.ID])

	withCounts, err := s.repo.ListActiveWithProviderCount(s.ctx)
	s.Require().NoError(err, "ListActiveWithProviderCount")
	s.Require().Len(withCounts, 2, "expected 2 proxies")
	for _, pc := range withCounts {
		switch pc.ID {
		case p1.ID:
			s.Require().Equal(int64(2), pc.ProviderCount, "p1 count mismatch")
		case p2.ID:
			s.Require().Equal(int64(1), pc.ProviderCount, "p2 count mismatch")
		default:
			s.Require().Fail("unexpected proxy id", pc.ID)
		}
	}
}

func (s *ProxyRepoSuite) mustCreateProxy(p *egress.Proxy) *egress.Proxy {
	s.T().Helper()
	s.Require().NoError(s.repo.Create(s.ctx, p), "create proxy")
	return p
}

func (s *ProxyRepoSuite) mustCreateProxyWithTimes(name, status string, createdAt time.Time) *egress.Proxy {
	s.T().Helper()

	// Set timestamps directly so ordering is deterministic across test runs.
	p := s.mustCreateProxy(&egress.Proxy{
		Name:     name,
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   status,
	})
	_, err := s.tx.ExecContext(s.ctx, "UPDATE proxies SET created_at = $1, updated_at = $1 WHERE id = $2", createdAt, p.ID)
	s.Require().NoError(err, "update proxy timestamps")
	return p
}

func (s *ProxyRepoSuite) mustInsertProvider(name string, proxyID *int64) {
	s.T().Helper()
	var pid any
	if proxyID != nil {
		pid = *proxyID
	}
	_, err := s.tx.ExecContext(
		s.ctx,
		"INSERT INTO providers (name, platform, type, proxy_id) VALUES ($1, $2, $3, $4)",
		name,
		capability.PlatformAnthropic,
		capability.ProviderTypeOAuth,
		pid,
	)
	s.Require().NoError(err, "insert provider")
}
