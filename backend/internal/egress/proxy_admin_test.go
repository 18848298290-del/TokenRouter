package egress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// proxyRepoStub injects provider-count and deletion results while rejecting
// unrelated repository operations in serial tests.
type proxyRepoStub struct {
	// countErr and providerCount select CountProvidersByProxyID output. deleteErr
	// controls Delete after it records the requested ID in deletedIDs, so failure
	// cases still expose the deletion attempt.
	deleteErr     error   // injected repository deletion error
	countErr      error   // injected provider-count query error
	providerCount int64   // configured bound provider count
	deletedIDs    []int64 // observed deletion attempt identities
}

// proxyRepoStubForAdminList records pagination/filter arguments and returns
// configured administration page outcomes.
type proxyRepoStubForAdminList struct {
	// listWithFiltersCalls, listWithFiltersParams, listWithFiltersProtocol,
	// listWithFiltersStatus, and listWithFiltersSearch capture ordinary listing
	// invocation. listWithFiltersErr, listWithFiltersProxies, and
	// listWithFiltersResult provide its outcome; nil metadata derives total and
	// page values from the configured slice and captured parameters.
	//
	// listWithFiltersAndProviderCountCalls, listWithFiltersAndProviderCountParams,
	// listWithFiltersAndProviderCountProtocol,
	// listWithFiltersAndProviderCountStatus, and
	// listWithFiltersAndProviderCountSearch capture augmented listing invocation.
	// listWithFiltersAndProviderCountErr, listWithFiltersAndProviderCountProxies,
	// and listWithFiltersAndProviderCountResult provide its outcome, using derived
	// pagination metadata when absent.
	// ProxyRepository supplies any methods outside the two fixture listing
	// overrides. The selected tests leave it nil and avoid invoking those delegated methods.
	ProxyRepository // unused repository operation delegation

	listWithFiltersCalls    int                          // observed page query count
	listWithFiltersParams   pagination.PaginationParams  // captured pagination and ordering values
	listWithFiltersProtocol string                       // captured protocol filter text
	listWithFiltersStatus   string                       // captured status filter text
	listWithFiltersSearch   string                       // captured name search text
	listWithFiltersProxies  []Proxy                      // configured returned proxy slice
	listWithFiltersResult   *pagination.PaginationResult // optional configured pagination result
	listWithFiltersErr      error                        // injected page query error

	listWithFiltersAndProviderCountCalls int // observed page query count
	// captured pagination and ordering values
	listWithFiltersAndProviderCountParams   pagination.PaginationParams
	listWithFiltersAndProviderCountProtocol string                   // captured protocol filter text
	listWithFiltersAndProviderCountStatus   string                   // captured status filter text
	listWithFiltersAndProviderCountSearch   string                   // captured name search text
	listWithFiltersAndProviderCountProxies  []ProxyWithProviderCount // configured returned proxy slice
	// optional configured pagination result
	listWithFiltersAndProviderCountResult *pagination.PaginationResult
	listWithFiltersAndProviderCountErr    error // injected page query error
}

// updatingProxyRepoStub overrides proxy retrieval and replacement to observe
// administration update delegation.
type updatingProxyRepoStub struct {
	*proxyRepoStub // shared base repository fixture
	// proxy supplies the current record copied during GetByID and replaced during
	// Update. Optional pointers remain shared with each shallow copy; the test
	// serializes those operations.
	proxy       *Proxy // shallow-copied current proxy value
	updateCalls int    // observed repository update count
}

// Create panics on an unexpected insert in this repository fixture. Deletion
// scenarios configure CountProvidersByProxyID and Delete, while the update
// wrapper supplies its own retrieval and replacement methods.
func (s *proxyRepoStub) Create(ctx context.Context, proxy *Proxy) error {
	panic("unexpected Create call")
}

// GetByID panics because this base fixture has no stored lookup behavior.
// Updating scenarios override retrieval in updatingProxyRepoStub, and deletion
// scenarios exercise provider-count lookup before attempting deletion.
func (s *proxyRepoStub) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	panic("unexpected GetByID call")
}

// ListByIDs panics when a test requests selected-record lookup through this
// fixture. The deletion scenarios pass IDs directly to the service and observe
// count and delete calls rather than retrieving records.
func (s *proxyRepoStub) ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	panic("unexpected ListByIDs call")
}

// Update panics because the base fixture does not implement record replacement.
// updatingProxyRepoStub overrides this method to count and retain update input
// in the dedicated administration update scenario.
func (s *proxyRepoStub) Update(ctx context.Context, proxy *Proxy) error {
	panic("unexpected Update call")
}

// Delete records the requested ID and then returns the injected deleteErr. A
// failed attempt remains visible in deletedIDs, allowing service error tests to
// distinguish invocation from successful deletion.
func (s *proxyRepoStub) Delete(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

// List panics when ordinary pagination reaches this fixture. Listing scenarios
// configure the separate proxyRepoStubForAdminList methods to capture filter
// arguments and return page results for their service calls.
func (s *proxyRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]Proxy, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics on an unexpected filtered page request through the
// base fixture. The administration listing scenario overrides this method in
// proxyRepoStubForAdminList to record arguments and supply results.
func (s *proxyRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]Proxy, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// ListActive panics when active-record lookup reaches this deletion-oriented
// fixture. Its supported count and deletion operations expose the chosen ID and
// injected failures needed by the service deletion scenarios.
func (s *proxyRepoStub) ListActive(ctx context.Context) ([]Proxy, error) {
	panic("unexpected ListActive call")
}

// ListActiveWithProviderCount panics because this fixture has no active
// augmented-list behavior. The selected service scenarios configure deletion,
// while augmented page tests use the separate listing fixture override.
func (s *proxyRepoStub) ListActiveWithProviderCount(ctx context.Context) ([]ProxyWithProviderCount, error) {
	panic("unexpected ListActiveWithProviderCount call")
}

// ListWithFiltersAndProviderCount panics on augmented page lookup through the
// base fixture. The provider-count listing scenario uses
// proxyRepoStubForAdminList to record ordering/filter inputs and return its
// configured augmented slice.
func (s *proxyRepoStub) ListWithFiltersAndProviderCount(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]ProxyWithProviderCount, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFiltersAndProviderCount call")
}

// ExistsByHostPortAuth panics if a scenario requests duplicate detection
// through this fixture. The deletion scenarios configure bound-provider count
// and delete responses and do not perform proxy creation matching.
func (s *proxyRepoStub) ExistsByHostPortAuth(ctx context.Context, host string, port int, username, password string) (bool, error) {
	panic("unexpected ExistsByHostPortAuth call")
}

// CountProvidersByProxyID returns the injected count error when present or the
// configured provider count. The fixture does not derive counts from IDs, so
// serial tests set the result for their service scenario.
func (s *proxyRepoStub) CountProvidersByProxyID(ctx context.Context, proxyID int64) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.providerCount, nil
}

// ListProviderSummariesByProxyID panics on an unexpected bound-provider summary
// query in this fixture. Count-based deletion checks use
// CountProvidersByProxyID and therefore expose no provider summary collection.
func (s *proxyRepoStub) ListProviderSummariesByProxyID(ctx context.Context, proxyID int64) ([]ProxyProviderSummary, error) {
	panic("unexpected ListProviderSummariesByProxyID call")
}

// SweepExpiredProxies returns no changed providers for the supplied fixture
// sweep inputs.
func (s *proxyRepoStub) SweepExpiredProxies(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// ListAllForFallback returns an absent candidate slice for fixture fallback
// lookup.
func (s *proxyRepoStub) ListAllForFallback(_ context.Context) ([]Proxy, error) {
	return nil, nil
}

// CountExpired returns zero stored expired proxies for this administration
// fixture.
func (s *proxyRepoStub) CountExpired(_ context.Context) (int64, error) {
	return 0, nil
}

// CountExpiringSoon returns zero warning-window proxies for this administration
// fixture.
func (s *proxyRepoStub) CountExpiringSoon(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// TestAdminService_DeleteProxy_Success configures no bound providers and a
// successful repository delete. It checks the service succeeds and records the
// selected ID in the fixture deletion attempts.
func TestAdminService_DeleteProxy_Success(t *testing.T) {
	repo := &proxyRepoStub{}
	svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

	err := svc.DeleteProxy(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.deletedIDs)
}

// TestAdminService_DeleteProxy_Idempotent supplies a successful fixture
// deletion for ID 404 with no bound providers. It checks service delegation and
// nil error; actual missing-record persistence behavior is outside this fixture
// assertion.
func TestAdminService_DeleteProxy_Idempotent(t *testing.T) {
	repo := &proxyRepoStub{}
	svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

	err := svc.DeleteProxy(context.Background(), 404)
	require.NoError(t, err)
	require.Equal(t, []int64{404}, repo.deletedIDs)
}

// TestAdminService_DeleteProxy_InUse configures two bound providers before
// deleting their proxy. It checks ErrProxyInUse is returned and the repository
// delete method is never invoked.
func TestAdminService_DeleteProxy_InUse(t *testing.T) {
	repo := &proxyRepoStub{providerCount: 2}
	svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

	err := svc.DeleteProxy(context.Background(), 77)
	require.ErrorIs(t, err, ErrProxyInUse)
	require.Empty(t, repo.deletedIDs)
}

// TestAdminService_DeleteProxy_Error injects a repository deletion failure
// after a zero provider count. It checks the service returns the same error so
// the administrator can observe the failed persistence operation.
func TestAdminService_DeleteProxy_Error(t *testing.T) {
	deleteErr := errors.New("delete failed")
	repo := &proxyRepoStub{deleteErr: deleteErr}
	svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

	err := svc.DeleteProxy(context.Background(), 33)
	require.ErrorIs(t, err, deleteErr)
}

// ListWithFilters records supplied page, ordering, and filter values before
// returning the configured slice or error. Without configured pagination
// metadata it derives total from the slice and copies page values; filtering
// and sorting are not performed by this fixture.
func (s *proxyRepoStubForAdminList) ListWithFilters(_ context.Context, params pagination.PaginationParams, protocol, status, search string) ([]Proxy, *pagination.PaginationResult, error) {
	s.listWithFiltersCalls++
	s.listWithFiltersParams = params
	s.listWithFiltersProtocol = protocol
	s.listWithFiltersStatus = status
	s.listWithFiltersSearch = search

	if s.listWithFiltersErr != nil {
		return nil, nil, s.listWithFiltersErr
	}

	result := s.listWithFiltersResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersProxies)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersProxies, result, nil
}

// ListWithFiltersAndProviderCount records supplied page, ordering, and filter
// values before returning the configured slice or error. Without configured
// pagination metadata it derives total from the slice and copies page values;
// filtering and sorting are not performed by this fixture.
func (s *proxyRepoStubForAdminList) ListWithFiltersAndProviderCount(_ context.Context, params pagination.PaginationParams, protocol, status, search string) ([]ProxyWithProviderCount, *pagination.PaginationResult, error) {
	s.listWithFiltersAndProviderCountCalls++
	s.listWithFiltersAndProviderCountParams = params
	s.listWithFiltersAndProviderCountProtocol = protocol
	s.listWithFiltersAndProviderCountStatus = status
	s.listWithFiltersAndProviderCountSearch = search

	if s.listWithFiltersAndProviderCountErr != nil {
		return nil, nil, s.listWithFiltersAndProviderCountErr
	}

	result := s.listWithFiltersAndProviderCountResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersAndProviderCountProxies)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersAndProviderCountProxies, result, nil
}

// TestAdminService_ListProxies_WithSearch supplies explicit pagination,
// ordering, and filter arguments with a configured fixture page. It checks
// returned proxies/total and every argument recorded by the ordinary filtered
// repository method.
func TestAdminService_ListProxies_WithSearch(t *testing.T) {
	t.Run("search 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &proxyRepoStubForAdminList{
			listWithFiltersProxies: []Proxy{{ID: 2, Name: "p1"}},
			listWithFiltersResult:  &pagination.PaginationResult{Total: 7},
		}
		svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

		proxies, total, err := svc.ListProxies(context.Background(), 3, 50, "http", billing.StatusActive, "p1", "name", "ASC")
		require.NoError(t, err)
		require.Equal(t, int64(7), total)
		require.Equal(t, []Proxy{{ID: 2, Name: "p1"}}, proxies)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 3, PageSize: 50, SortBy: "name", SortOrder: "ASC"}, repo.listWithFiltersParams)
		require.Equal(t, "http", repo.listWithFiltersProtocol)
		require.Equal(t, billing.StatusActive, repo.listWithFiltersStatus)
		require.Equal(t, "p1", repo.listWithFiltersSearch)
	})
}

// TestAdminService_ListProxiesWithProviderCount_WithSearch supplies
// provider-count ordering and a configured augmented page. It checks returned
// proxy/count data plus the pagination and protocol/status/search values
// captured by the repository fixture.
func TestAdminService_ListProxiesWithProviderCount_WithSearch(t *testing.T) {
	t.Run("search 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &proxyRepoStubForAdminList{
			listWithFiltersAndProviderCountProxies: []ProxyWithProviderCount{{Proxy: Proxy{ID: 3, Name: "p2"}, ProviderCount: 5}},
			listWithFiltersAndProviderCountResult:  &pagination.PaginationResult{Total: 9},
		}
		svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

		proxies, total, err := svc.ListProxiesWithProviderCount(context.Background(), 2, 10, "socks5", billing.StatusDisabled, "p2", "provider_count", "DESC")
		require.NoError(t, err)
		require.Equal(t, int64(9), total)
		require.Equal(t, []ProxyWithProviderCount{{Proxy: Proxy{ID: 3, Name: "p2"}, ProviderCount: 5}}, proxies)

		require.Equal(t, 1, repo.listWithFiltersAndProviderCountCalls)
		require.Equal(t, pagination.PaginationParams{Page: 2, PageSize: 10, SortBy: "provider_count", SortOrder: "DESC"}, repo.listWithFiltersAndProviderCountParams)
		require.Equal(t, "socks5", repo.listWithFiltersAndProviderCountProtocol)
		require.Equal(t, billing.StatusDisabled, repo.listWithFiltersAndProviderCountStatus)
		require.Equal(t, "p2", repo.listWithFiltersAndProviderCountSearch)
	})
}

// TestFinalizeProxyQualityResult_ScoreAndGrade combines pass, warning, failure,
// and challenge counts before finalization. It checks the weighted score of 38,
// F grade, and each count fragment in the summary.
func TestFinalizeProxyQualityResult_ScoreAndGrade(t *testing.T) {
	result := &ProxyQualityCheckResult{
		PassedCount:    2,
		WarnCount:      1,
		FailedCount:    1,
		ChallengeCount: 1,
	}

	finalizeProxyQualityResult(result)

	require.Equal(t, 38, result.Score)
	require.Equal(t, "F", result.Grade)
	require.Contains(t, result.Summary, "Passed: 2")
	require.Contains(t, result.Summary, "warnings: 1")
	require.Contains(t, result.Summary, "failed: 1")
	require.Contains(t, result.Summary, "challenges: 1")
}

// GetByID returns a shallow struct copy of the fixture's current proxy for
// administration updates. Optional pointer values remain shared; the serial
// scenario verifies the service changes the host through repository
// replacement.
func (s *updatingProxyRepoStub) GetByID(context.Context, int64) (*Proxy, error) {
	copy := *s.proxy
	return &copy, nil
}

// Update increments its invocation count and retains a shallow copy of the
// supplied proxy before returning success. Optional pointers remain shared,
// while the test inspects the copied host after the service call.
func (s *updatingProxyRepoStub) Update(_ context.Context, proxy *Proxy) error {
	s.updateCalls++
	copy := *proxy
	s.proxy = &copy
	return nil
}

// TestProxyAdminUpdateUsesRepositoryUpdate patches an existing host while
// carrying expiration fallback settings. It checks the service invokes
// repository Update once and the retained fixture record contains the new host.
func TestProxyAdminUpdateUsesRepositoryUpdate(t *testing.T) {
	t.Run("adminService", func(t *testing.T) {
		repo := &updatingProxyRepoStub{
			proxyRepoStub: &proxyRepoStub{},
			proxy: &Proxy{
				ID:             9,
				Protocol:       "http",
				Host:           "old.example",
				Port:           8080,
				Status:         billing.StatusActive,
				FallbackMode:   FallbackModeNone,
				ExpiryWarnDays: 7,
			},
		}
		svc := NewProxyAdmin(repo, nil, nil, nil, ProxyAdminOptions{})

		_, err := svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{
			Host:           "new.example",
			FallbackMode:   FallbackModeNone,
			ExpiryWarnDays: 7,
		})

		require.NoError(t, err)
		require.Equal(t, 1, repo.updateCalls)
		require.Equal(t, "new.example", repo.proxy.Host)
	})
}
