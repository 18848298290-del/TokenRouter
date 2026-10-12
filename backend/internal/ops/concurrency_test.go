package ops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

type opsProviderStatsRepoStub struct {
	ProviderReader
	providers      []ProviderObservation
	platformFilter string
	groupIDFilter  *int64
}

type opsProviderStatsFallbackRepoStub struct {
	ProviderReader
	platformFilter string
	groupIDFilter  int64
}

// ListOpsProvidersForStats records query filters to verify that the service uses the statistics query.
func (r *opsProviderStatsRepoStub) ListOpsProvidersForStats(_ context.Context, platformFilter string, groupIDFilter *int64) ([]ProviderObservation, error) {
	r.platformFilter = platformFilter
	r.groupIDFilter = groupIDFilter
	return r.providers, nil
}

// ListPage simulates a repository that supports only the paginated interface.
func (r *opsProviderStatsFallbackRepoStub) ListPage(
	_ context.Context,
	params pagination.PaginationParams,
	platform string,
	groupID int64,
) ([]ProviderObservation, *pagination.PaginationResult, error) {
	r.platformFilter = platform
	r.groupIDFilter = groupID
	providers := []ProviderObservation{{ID: 1, Name: "provider-1"}}
	return providers, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Total: 1}, nil
}

func TestListAllProvidersForOpsUsesLightweightRepository(t *testing.T) {
	groupID := int64(42)
	repo := &opsProviderStatsRepoStub{providers: []ProviderObservation{{ID: 1}}}
	service := &OpsService{providerRepo: repo}

	providers, err := service.listAllProvidersForOps(context.Background(), PlatformOpenAI, &groupID)

	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, PlatformOpenAI, repo.platformFilter)
	require.Same(t, &groupID, repo.groupIDFilter)
}

func TestListAllProvidersForOpsFallbackPassesGroupFilter(t *testing.T) {
	groupID := int64(77)
	repo := &opsProviderStatsFallbackRepoStub{}
	service := &OpsService{providerRepo: repo}

	providers, err := service.listAllProvidersForOps(context.Background(), PlatformAnthropic, &groupID)

	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, PlatformAnthropic, repo.platformFilter)
	require.Equal(t, groupID, repo.groupIDFilter)
}

func TestGetProviderAvailabilityStatsOnlyAggregatesSelectedGroup(t *testing.T) {
	targetGroupID := int64(7)
	otherGroup := &GroupObservation{ID: 8, Name: "其他分组"}
	targetGroup := &GroupObservation{ID: targetGroupID, Name: "目标分组"}
	repo := &opsProviderStatsRepoStub{providers: []ProviderObservation{
		{
			ID:          11,
			Name:        "多分组提供商",
			Platform:    PlatformAnthropic,
			Status:      StatusActive,
			Schedulable: true,
			Groups:      []*GroupObservation{otherGroup, targetGroup},
		},
	}}
	service := &OpsService{providerRepo: repo}

	_, groups, providers, _, err := service.GetProviderAvailabilityStats(
		context.Background(),
		PlatformAnthropic,
		&targetGroupID,
	)

	require.NoError(t, err)
	require.Contains(t, groups, targetGroupID)
	require.NotContains(t, groups, otherGroup.ID)
	require.Len(t, groups, 1)
	require.Equal(t, targetGroupID, providers[11].GroupID)
	require.Equal(t, targetGroup.Name, providers[11].GroupName)
}
