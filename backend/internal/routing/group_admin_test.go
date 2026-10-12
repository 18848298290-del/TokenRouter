package routing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// groupRepoStubForAdmin records repository reads and writes for administrative tests.
type groupRepoStubForAdmin struct {
	created *Group // Arguments received by Create.
	updated *Group // Arguments received by Update.
	getByID *Group // Result returned by GetByID.
	getErr  error  // Error returned by GetByID.

	listWithFiltersCalls       int
	listWithFiltersParams      pagination.PaginationParams
	listWithFiltersPlatform    string
	listWithFiltersStatus      string
	listWithFiltersSearch      string
	listWithFiltersIsExclusive *bool
	listWithFiltersGroups      []Group
	listWithFiltersResult      *pagination.PaginationResult
	listWithFiltersErr         error
	groupSortOrderLockCalls    int
}

type groupProviderCopyRepoStub struct {
	*groupRepoStubForAdmin
	groupsByID        map[int64]*Group
	sourceProviderIDs []int64
	deletedGroupID    int64
	boundGroupID      int64
	boundProviderIDs  []int64
}

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

func (s *groupProviderCopyRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group := s.groupsByID[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

func (s *groupProviderCopyRepoStub) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return s.GetByID(ctx, id)
}

func (s *groupProviderCopyRepoStub) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	return append([]int64(nil), s.sourceProviderIDs...), nil
}

func (s *groupProviderCopyRepoStub) DeleteProviderGroupsByGroupID(_ context.Context, groupID int64) (int64, error) {
	s.deletedGroupID = groupID
	return 1, nil
}

func (s *groupProviderCopyRepoStub) BindProvidersToGroup(_ context.Context, groupID int64, providerIDs []int64) error {
	s.boundGroupID = groupID
	s.boundProviderIDs = append([]int64(nil), providerIDs...)
	return nil
}

func (s *groupRepoStubForAdmin) Create(_ context.Context, g *Group) error {
	s.created = g
	return nil
}

func (s *groupRepoStubForAdmin) Update(_ context.Context, g *Group) error {
	s.updated = g
	return nil
}

func (s *groupRepoStubForAdmin) GetByID(_ context.Context, _ int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

func (s *groupRepoStubForAdmin) GetByIDLite(_ context.Context, _ int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

func (s *groupRepoStubForAdmin) Delete(_ context.Context, _ int64) error {
	panic("unexpected Delete call")
}

func (s *groupRepoStubForAdmin) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

func (s *groupRepoStubForAdmin) List(_ context.Context, _ pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

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

func (s *groupRepoStubForAdmin) ListActive(_ context.Context) ([]Group, error) {
	panic("unexpected ListActive call")
}

func (s *groupRepoStubForAdmin) ListActiveByPlatform(_ context.Context, _ string) ([]Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

func (s *groupRepoStubForAdmin) ListActiveByPlatformLite(_ context.Context, _ string) ([]Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

func (s *groupRepoStubForAdmin) ExistsByName(_ context.Context, _ string) (bool, error) {
	panic("unexpected ExistsByName call")
}

func (s *groupRepoStubForAdmin) GetProviderCount(_ context.Context, _ int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

func (s *groupRepoStubForAdmin) DeleteProviderGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

func (s *groupRepoStubForAdmin) BindProvidersToGroup(_ context.Context, _ int64, _ []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

func (s *groupRepoStubForAdmin) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

func (s *groupRepoStubForAdmin) UpdateSortOrders(_ context.Context, _ []GroupSortOrderUpdate) error {
	return nil
}

// LockGroupSortOrder records whether creation requested the ordering lock.
func (s *groupRepoStubForAdmin) LockGroupSortOrder(_ context.Context) error {
	s.groupSortOrderLockCalls++
	return nil
}
