package ops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// opsProviderStatsRepoStub returns fixture observations and captures direct
// statistics query selections.
type opsProviderStatsRepoStub struct {
	// ProviderReader leaves ordinary provider methods unset. The fixture
	// implements ListOpsProvidersForStats directly, so the service can select that
	// method while pagination remains unconfigured.
	ProviderReader // unused embedded provider methods
	// providers is returned as the configured fixture slice without copying its
	// entries. Test callers retain the fixture while inspecting the resulting
	// service view.
	providers      []ProviderObservation // configured provider observation slice
	platformFilter string                // last requested platform text
	// groupIDFilter retains the pointer passed to the statistics query. The
	// pointer identity assertion checks that the service forwards this selection
	// directly.
	groupIDFilter *int64 // last requested group pointer
}

// opsProviderStatsFallbackRepoStub captures selections for the paginated
// provider-reader compatibility path.
type opsProviderStatsFallbackRepoStub struct {
	// ProviderReader supplies remaining methods through its nil embedded
	// interface. The fixture overrides ListPage for the compatibility path;
	// another provider operation needs a configured implementation.
	ProviderReader        // unused embedded provider methods
	platformFilter string // last requested platform text
	groupIDFilter  int64  // last requested group value
}

// ListOpsProvidersForStats records platform text and the supplied group pointer
// before returning the fixture provider slice. Context is ignored, and the
// returned fixture entries remain shared with the test setup.
func (r *opsProviderStatsRepoStub) ListOpsProvidersForStats(_ context.Context, platformFilter string, groupIDFilter *int64) ([]ProviderObservation, error) {
	r.platformFilter = platformFilter
	r.groupIDFilter = groupIDFilter
	return r.providers, nil
}

// ListPage records the platform and group value and returns one provider with
// matching pagination metadata. Its total stops service pagination after one
// call, allowing the test to isolate group-value forwarding.
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

// TestListAllProvidersForOpsUsesLightweightRepository supplies a reader with
// the optional statistics method. It checks fixture results, platform
// forwarding, and retention of the exact group-filter pointer passed by the
// caller.
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

// TestListAllProvidersForOpsFallbackPassesGroupFilter supplies a reader
// supporting the paginated path. It checks the returned provider and that the
// dereferenced positive group selection reaches ListPage with the requested
// platform.
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

// TestGetProviderAvailabilityStatsOnlyAggregatesSelectedGroup returns one
// provider whose first membership differs from the selected group. It checks
// that the selected aggregate group and displayed identity use that
// membership while the other group is absent.
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
