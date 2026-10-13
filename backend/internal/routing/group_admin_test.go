package routing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Copying provider memberships during a group update sends provider IDs to
// the destination binding operation. The internal test uses raw group
// pointers and a direct mutation callback to inspect clearing and rebinding,
// including the returned destination identity.
//
// The derived repository supplies source and target reads while recording the
// deletion target and a copied binding argument. Its base fixture captures
// ordinary write and listing calls, synthesizing page metadata when none is
// supplied. The assertion checks membership replacement for one successful
// source and two provider IDs; transaction rollback and persistence are
// supplied by other repositories, so this fixture's observations concern the
// administrative calls themselves.

// groupRepoStubForAdmin captures raw group writes and supplies configured
// reads for administrative tests.
type groupRepoStubForAdmin struct {
	// created and updated retain the supplied write pointers. getByID supplies
	// the read pointer, while getErr takes precedence over that result in both
	// ID readers.
	created *Group // recorded group creation argument
	updated *Group // recorded group update argument
	getByID *Group // supplied group read result
	// getErr makes both ID readers return nil and the supplied failure, taking
	// precedence over the configured getByID pointer.
	getErr error // injected group read error

	listWithFiltersCalls    int                         // recorded filtered listing count
	listWithFiltersParams   pagination.PaginationParams // captured filtered page options
	listWithFiltersPlatform string                      // captured platform filter text
	listWithFiltersStatus   string                      // captured status filter text
	listWithFiltersSearch   string                      // captured search filter text
	// listWithFiltersIsExclusive retains the caller's pointer for later
	// assertions; tests keep that pointed value stable until the listing request
	// has been inspected.
	listWithFiltersIsExclusive *bool // captured optional exclusivity filter
	// listWithFiltersGroups is returned directly. Callers observe these fixture
	// rows through the surrounding service or adapter and configure their
	// contents before use.
	listWithFiltersGroups []Group // supplied filtered listing rows
	// listWithFiltersResult overrides synthesized pagination when nonnil.
	// Otherwise ListWithFilters derives the total from listWithFiltersGroups and
	// copies page values from the request.
	listWithFiltersResult *pagination.PaginationResult // optional supplied page metadata
	// listWithFiltersErr suppresses rows and pagination after query arguments
	// have been recorded, allowing assertions to inspect a failed listing's
	// request.
	listWithFiltersErr      error // injected filtered listing error
	groupSortOrderLockCalls int   // recorded ordering lock requests
}

// groupProviderCopyRepoStub supplies source groups and records destination
// membership replacement calls.
type groupProviderCopyRepoStub struct {
	*groupRepoStubForAdmin // base administrative repository fixture
	// groupsByID returns retained pointers for both source and target reads.
	// UpdateGroup therefore mutates the target object observed by the test's
	// identity assertion.
	groupsByID        map[int64]*Group // supplied source and destination groups
	sourceProviderIDs []int64          // supplied source provider memberships
	deletedGroupID    int64            // captured membership deletion target
	boundGroupID      int64            // captured membership binding target
	// boundProviderIDs copies the binding argument so later caller slice changes
	// cannot alter the membership values inspected by the test.
	boundProviderIDs []int64 // copied bound provider IDs
}

// TestAdminServiceUpdateGroupCopiesMembershipWithoutAssociationPriority
// updates a destination with memberships from one source group. It checks the
// same destination pointer is returned, existing memberships are cleared, and
// the two supplied provider IDs are rebound.
func TestAdminServiceUpdateGroupCopiesMembershipWithoutAssociationPriority(t *testing.T) {
	target := &Group{ID: 1701, Name: "target", Status: StatusActive}
	source := &Group{ID: 1702, Name: "source", Status: StatusActive}
	base := &groupRepoStubForAdmin{}
	repo := &groupProviderCopyRepoStub{
		groupRepoStubForAdmin: base,
		groupsByID:            map[int64]*Group{target.ID: target, source.ID: source},
		sourceProviderIDs:     []int64{71, 72},
	}
	svc := &GroupAdmin{groupRepo: repo, options: GroupAdminOptions{Mutate: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }}}

	updated, err := svc.UpdateGroup(context.Background(), target.ID, &UpdateGroupInput{
		CopyProvidersFromGroupIDs: []int64{source.ID},
	})

	require.NoError(t, err)
	require.Same(t, target, updated)
	require.Equal(t, target.ID, repo.deletedGroupID)
	require.Equal(t, target.ID, repo.boundGroupID)
	require.Equal(t, []int64{71, 72}, repo.boundProviderIDs)
}

// GetByID returns the retained group pointer for a configured ID. A missing
// or nil entry returns ErrGroupNotFound, allowing source validation to use
// the fixture's actual lookup.
func (s *groupProviderCopyRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group := s.groupsByID[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

// GetByIDLite delegates to the same retained group lookup as GetByID. It
// forwards context and ID, so lightweight source validation observes that
// lookup's configured groups and ErrGroupNotFound results.
func (s *groupProviderCopyRepoStub) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return s.GetByID(ctx, id)
}

// GetProviderIDsByGroupIDs returns a copied source-provider slice and ignores
// the requested groups. Empty configured memberships yield nil; changing
// returned elements cannot alter the fixture's stored provider IDs.
func (s *groupProviderCopyRepoStub) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	return append([]int64(nil), s.sourceProviderIDs...), nil
}

// DeleteProviderGroupsByGroupID records the destination ID and reports one
// removed membership. It succeeds for every request, making the clearing step
// observable before the test inspects rebinding.
func (s *groupProviderCopyRepoStub) DeleteProviderGroupsByGroupID(_ context.Context, groupID int64) (int64, error) {
	s.deletedGroupID = groupID
	return 1, nil
}

// BindProvidersToGroup records the destination and copies the supplied
// provider IDs. The method succeeds for every request, and later caller
// mutations cannot change the captured binding slice.
func (s *groupProviderCopyRepoStub) BindProvidersToGroup(_ context.Context, groupID int64, providerIDs []int64) error {
	s.boundGroupID = groupID
	s.boundProviderIDs = append([]int64(nil), providerIDs...)
	return nil
}

// Create retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured creation argument.
func (s *groupRepoStubForAdmin) Create(_ context.Context, g *Group) error {
	s.created = g
	return nil
}

// Update retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured update argument.
func (s *groupRepoStubForAdmin) Update(_ context.Context, g *Group) error {
	s.updated = g
	return nil
}

// GetByID returns the configured group pointer unless getErr is set. It
// ignores the requested ID, and callers receive the same object retained in
// the fixture.
func (s *groupRepoStubForAdmin) GetByID(_ context.Context, _ int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

// GetByIDLite returns the configured group pointer unless getErr is set. It
// ignores the requested ID and supplies the same fixture value as the full
// reader.
func (s *groupRepoStubForAdmin) GetByIDLite(_ context.Context, _ int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

// Delete panics when administration tests request direct group deletion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) Delete(_ context.Context, _ int64) error {
	panic("unexpected Delete call")
}

// DeleteCascade panics when create or update fixtures receive a cascade
// deletion. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

// List panics when administration requests an unfiltered paginated listing.
// The fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) List(_ context.Context, _ pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters records its pagination and filter inputs before returning
// the configured rows. An injected error suppresses rows and pagination;
// absent result metadata is synthesized from the row count and request page
// values.
func (s *groupRepoStubForAdmin) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]Group, *pagination.PaginationResult, error) {
	s.listWithFiltersCalls++
	s.listWithFiltersParams = params
	s.listWithFiltersPlatform = platform
	s.listWithFiltersStatus = status
	s.listWithFiltersSearch = search
	s.listWithFiltersIsExclusive = isExclusive

	if s.listWithFiltersErr != nil {
		return nil, nil, s.listWithFiltersErr
	}

	result := s.listWithFiltersResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersGroups)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersGroups, result, nil
}

// ListActive panics when administration requests all active groups through
// this fixture. The fixture has no implementation for this repository path,
// so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ListActive(_ context.Context) ([]Group, error) {
	panic("unexpected ListActive call")
}

// ListActiveByPlatform panics when administration requests active groups
// filtered by platform. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ListActiveByPlatform(_ context.Context, _ string) ([]Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

// ListActiveByPlatformLite panics when administration requests lightweight
// active groups filtered by platform. The fixture has no implementation for
// this repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) ListActiveByPlatformLite(_ context.Context, _ string) ([]Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

// ExistsByName panics when administration requests a group-name existence
// check. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ExistsByName(_ context.Context, _ string) (bool, error) {
	panic("unexpected ExistsByName call")
}

// GetProviderCount panics when administration requests separate
// provider-count hydration. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) GetProviderCount(_ context.Context, _ int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

// DeleteProviderGroupsByGroupID panics when the base fixture receives
// membership replacement cleanup. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) DeleteProviderGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

// BindProvidersToGroup panics when the base fixture receives new provider
// membership binding. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) BindProvidersToGroup(_ context.Context, _ int64, _ []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

// GetProviderIDsByGroupIDs panics when the base fixture receives source
// membership lookup. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

// UpdateSortOrders accepts ordering writes successfully and retains no update
// arguments.
func (s *groupRepoStubForAdmin) UpdateSortOrders(_ context.Context, _ []GroupSortOrderUpdate) error {
	return nil
}

// LockGroupSortOrder counts each ordering-lock request and returns successful
// acquisition.
func (s *groupRepoStubForAdmin) LockGroupSortOrder(_ context.Context) error {
	s.groupSortOrderLockCalls++
	return nil
}
