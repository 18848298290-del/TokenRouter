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

// Proxy listings combine stored connection attributes with counts of provider
// references. PostgreSQL fixtures exercise creation, editing, deletion,
// filters, authentication identity, and aggregation, with an independent
// rollback-owned transaction for each suite test.

// ProxyRepoSuite exercises proxy persistence and provider counts within
// rollback-owned PostgreSQL transactions.
type ProxyRepoSuite struct {
	suite.Suite // test assertions and runner
	// ctx, tx, and repo share one transaction for the current suite test.
	// Proxy rows and provider references remain inside that transaction until
	// testEntTx's registered cleanup rolls it back.
	ctx  context.Context      // current proxy-test query context
	tx   *dbent.Tx            // rollback-owned PostgreSQL fixture transaction
	repo *postgres.ProxyStore // proxy store under test
}

// SetupTest starts a fresh rollback-owned transaction and builds the proxy
// store on its client and executor. Provider cleanup and outbox participation
// use the same transaction for each suite test.
func (s *ProxyRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.tx = tx
	s.repo = newProxyStoreContract(tx.Client(), tx)
}

// TestProxyRepoSuite runs proxy persistence, filtering, and provider-reference
// aggregation cases on PostgreSQL. A new transaction isolates each test so rows
// and counts from another case cannot affect its assertions.
func TestProxyRepoSuite(t *testing.T) {
	suite.Run(t, new(ProxyRepoSuite))
}

// TestCreate inserts a proxy through the repository and reads it back. The
// assigned ID must be nonzero and the stored name must match the input.
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

// TestGetByID_NotFound queries an empty proxy fixture for an ID that was never
// inserted. The repository must return an error for that missing identifier.
func (s *ProxyRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

// TestUpdate creates a proxy, changes its name, and saves it through the
// repository. A subsequent read of the same ID must expose the requested name.
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

// TestDelete creates a proxy and deletes it through the repository. Reading its
// assigned ID afterward must return an error, making repository-visible
// deletion observable.
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

// TestList inserts two proxies before requesting a page larger than the fixture
// set. The list contains both rows and reports a total count of two.
func (s *ProxyRepoSuite) TestList() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(proxies, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListWithFilters_Protocol creates HTTP and SOCKS5 proxies before selecting
// the SOCKS5 protocol. Exactly one row is returned and its stored protocol
// matches that filter.
func (s *ProxyRepoSuite) TestListWithFilters_Protocol() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "socks5", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "socks5", "", "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal("socks5", proxies[0].Protocol)
}

// TestListWithFilters_Status creates active and disabled proxies before
// selecting disabled status. Exactly one row is returned and its stored status
// matches the requested value.
func (s *ProxyRepoSuite) TestListWithFilters_Status() {
	s.mustCreateProxy(&egress.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusDisabled})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", billing.StatusDisabled, "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal(billing.StatusDisabled, proxies[0].Status)
}

// TestListWithFilters_Search creates production and development names before
// searching for the production substring. The returned list has one row whose
// name contains the expected text.
func (s *ProxyRepoSuite) TestListWithFilters_Search() {
	s.mustCreateProxy(&egress.Proxy{Name: "production-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "dev-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "prod")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Contains(proxies[0].Name, "production")
}

// TestListActive inserts an active proxy and a disabled proxy before querying
// active rows. Exactly the named active fixture is returned by the repository.
func (s *ProxyRepoSuite) TestListActive() {
	s.mustCreateProxy(&egress.Proxy{Name: "active1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustCreateProxy(&egress.Proxy{Name: "inactive1", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: billing.StatusDisabled})

	proxies, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(proxies, 1)
	s.Require().Equal("active1", proxies[0].Name)
}

// TestExistsByHostPortAuth stores a proxy with username and password, then
// checks matching and different credentials. The exact tuple exists while the
// altered authentication tuple does not.
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

// TestExistsByHostPortAuth_NoAuth persists a proxy whose username and password
// are empty. Looking up its host, port, and empty authentication values reports
// the fixture as existing.
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

// TestCountProvidersByProxyID inserts two providers referencing one proxy and
// another provider with no proxy. The requested reference count includes the
// linked pair and equals two.
func (s *ProxyRepoSuite) TestCountProvidersByProxyID() {
	proxy := s.mustCreateProxy(&egress.Proxy{Name: "p-count", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})
	s.mustInsertProvider("a1", &proxy.ID)
	s.mustInsertProvider("a2", &proxy.ID)
	s.mustInsertProvider("a3", nil)

	count, err := s.repo.CountProvidersByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err, "CountProvidersByProxyID")
	s.Require().Equal(int64(2), count)
}

// TestCountProvidersByProxyID_Zero creates a proxy with no provider references
// in the transaction. The repository returns a successful count of zero for
// that proxy.
func (s *ProxyRepoSuite) TestCountProvidersByProxyID_Zero() {
	proxy := s.mustCreateProxy(&egress.Proxy{Name: "p-zero", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive})

	count, err := s.repo.CountProvidersByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Zero(count)
}

// TestGetProviderCountsForProxies gives one proxy two provider references and
// another proxy one reference. The aggregate map reports the corresponding
// counts under their database IDs.
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

// TestGetProviderCountsForProxies_Empty queries provider-reference aggregation
// before any fixture rows are inserted. The repository returns an empty count
// map and no error for that transaction.
func (s *ProxyRepoSuite) TestGetProviderCountsForProxies_Empty() {
	counts, err := s.repo.GetProviderCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Empty(counts)
}

// TestListActiveWithProviderCount creates differently dated active proxies, a
// disabled proxy, and provider references. Active results follow newest
// creation time first and expose each proxy's expected provider count.
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

	// Fixed creation times put the second active proxy first, while its lower
	// provider count distinguishes creation-time ordering from sorting by usage count.
	s.Require().Equal(p2.ID, withCounts[0].ID)
	s.Require().Equal(int64(1), withCounts[0].ProviderCount)
	s.Require().Equal(p1.ID, withCounts[1].ID)
	s.Require().Equal(int64(2), withCounts[1].ProviderCount)
}

// TestExistsByHostPortAuth_And_ProviderCountAggregates combines authentication
// lookup with single-proxy, map, and list count queries. All paths agree on the
// two-to-one provider distribution assigned to the fixture proxies.
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

// mustCreateProxy persists the supplied proxy and returns the same object after
// its ID is filled. Database errors fail the test, and the suite transaction
// owns cleanup of the inserted row.
func (s *ProxyRepoSuite) mustCreateProxy(p *egress.Proxy) *egress.Proxy {
	s.T().Helper()
	s.Require().NoError(s.repo.Create(s.ctx, p), "create proxy")
	return p
}

// mustCreateProxyWithTimes creates a proxy, then writes its creation and update
// timestamps directly for deterministic ordering. The returned object contains
// its assigned ID; callers query storage when they need the overwritten
// timestamps.
func (s *ProxyRepoSuite) mustCreateProxyWithTimes(name, status string, createdAt time.Time) *egress.Proxy {
	s.T().Helper()

	// Write stored timestamps after creation because clock-assigned values cannot
	// establish the intended ordering reliably when fixtures are inserted close together.
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

// mustInsertProvider creates a provider using the given name and optional proxy
// reference inside the suite transaction. A nil proxyID stores SQL NULL; insert
// errors fail the case.
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
