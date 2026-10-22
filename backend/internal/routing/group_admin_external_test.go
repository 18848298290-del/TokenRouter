package routing_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrative group inputs distinguish omission from explicit clearing and
// normalize named policies, client protocols, scheduler settings, and
// fallback references before saving. The fixtures capture repository
// arguments and authentication invalidation requests so each scenario can
// inspect the values sent to those dependencies.
//
// Model-candidate tests supply concrete provider rules and independent group
// policy through the shared test constructor, including custom-list filtering
// and different allowlist stages. Fallback fixtures return configured target
// pointers and synthesize the automatic sort tail from their group map.
// Platform words in historical test names or subtest labels do not add a
// platform input to these requests, and repository spies expose successful
// call flow rather than database transaction behavior.

// groupRepoStub records cascade deletion attempts and returns supplied
// affected-user results.
type groupRepoStub struct {
	// affectedUserIDs is returned directly by DeleteCascade, including with an
	// error. DeleteGroup ignores that result and the deletion tests inspect its
	// recorded group call.
	affectedUserIDs []int64 // supplied cascade-affected user IDs
	// deleteErr is returned after DeleteCascade records its group request. The
	// affectedUserIDs slice remains supplied alongside that error so the fixture
	// can represent both results.
	deleteErr   error   // injected cascade deletion error
	deleteCalls []int64 // recorded cascade group requests
}

// deleteGroupAPIKeyRepoStub records group key-list requests and supplies
// bound credential strings.
type deleteGroupAPIKeyRepoStub struct {
	// keys is returned directly when listErr is nil. Tests keep its backing
	// storage stable while DeleteGroup captures the strings for later
	// invalidation.
	keys []string // supplied bound credential strings
	// listErr makes ListKeysByGroupID return nil keys after recording the group
	// query, allowing the fixture to represent an unsuccessful key capture.
	listErr      error   // injected key listing error
	listGroupIDs []int64 // recorded key-list group requests
}

// groupModelsListProviderRepoStub supplies catalogue provider fixtures and
// records the requested group.
type groupModelsListProviderRepoStub struct {
	// GroupProviders supplies unused methods through a nil embedded interface.
	// Calling a promoted method without a fixture override panics.
	routing.GroupProviders // unused provider reader methods
	// providers supplies an outer-slice copy for each query. Empty Type values
	// are filled on the copy, while credentials and other nested data remain
	// shared.
	providers     []provider.Record // configured catalogue provider records
	calledGroupID int64             // latest requested catalogue group ID
}

// authCacheInvalidatorStub records group and credential snapshot invalidation
// calls from administration.
type authCacheInvalidatorStub struct {
	groupIDs []int64  // recorded group snapshot requests
	keys     []string // recorded credential snapshot requests
}

// groupRepoStubForFallbackCycle supplies configured target links for
// client-fallback cycle detection.
type groupRepoStubForFallbackCycle struct {
	// groups returns retained group pointers for existing keys. Tests configure
	// the chain before validation, and an absent key produces ErrGroupNotFound.
	groups map[int64]*routing.Group // configured fallback chain groups
}

// groupRepoStubForInvalidRequestFallback supplies target reads, captured
// writes, and automatic sort-tail rows.
type groupRepoStubForInvalidRequestFallback struct {
	// groups supplies retained read pointers and the automatic sort tail.
	// created and updated record the write pointers passed through the shared
	// test constructor's cloning adapter.
	groups  map[int64]*routing.Group // configured destination and fallback groups
	created *routing.Group           // captured group creation pointer
	updated *routing.Group           // captured group update pointer
}

// groupPlatformRepoStub supplies shallow group reads and an optional recorded
// update failure.
type groupPlatformRepoStub struct {
	// GroupRepository supplies unused methods through a nil embedded interface.
	// Calling a promoted method without a fixture override panics.
	routing.GroupRepository // unused group repository methods

	// group is shallow-copied during GetByID. Nested references remain shared
	// with the fixture, while scalar edits occur on the returned copy used by
	// administration.
	group *routing.Group // configured nonnil group value
	// updated retains the successful write argument directly. Tests inspect its
	// fields while that working group remains stable after Update returns.
	updated *routing.Group // captured successful group update
	// updateErr returns from Update before updated is assigned, allowing the
	// fixture to distinguish rejected writes from pointers retained after a
	// successful call.
	updateErr error // injected group update error
}

// pricingConfigCacheInvalidatorSpy counts pricing invalidation requests made
// by the administrative service.
type pricingConfigCacheInvalidatorSpy struct {
	calls int // recorded pricing invalidation count
}

// TestAdminService_DeleteGroup_Success supplies a successful cascade deletion
// with affected user IDs. The call returns no error and records the requested
// group ID once.
func TestAdminService_DeleteGroup_Success(t *testing.T) {
	repo := &groupRepoStub{affectedUserIDs: []int64{11, 12}}
	svc := newGroupAdminForTest(repo, nil, nil)

	err := svc.DeleteGroup(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []int64{5}, repo.deleteCalls)
}

// TestAdminService_DeleteGroup_InvalidatesAuthCacheForBoundKeys supplies two
// keys bound to the deleted group. It checks the key-list query, cascade
// call, and both recorded key invalidations.
func TestAdminService_DeleteGroup_InvalidatesAuthCacheForBoundKeys(t *testing.T) {
	repo := &groupRepoStub{}
	apiKeyRepo := &deleteGroupAPIKeyRepoStub{keys: []string{"k1", "k2"}}
	invalidator := &authCacheInvalidatorStub{}
	svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, invalidator, nil, apiKeyRepo)

	err := svc.DeleteGroup(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []int64{5}, repo.deleteCalls)
	require.Equal(t, []int64{5}, apiKeyRepo.listGroupIDs)
	require.Equal(t, []string{"k1", "k2"}, invalidator.keys)
}

// TestAdminService_DeleteGroup_NotFound makes cascade deletion return
// ErrGroupNotFound for requested group 99. The administrative call exposes
// that same missing-group sentinel through errors.Is.
func TestAdminService_DeleteGroup_NotFound(t *testing.T) {
	repo := &groupRepoStub{deleteErr: routing.ErrGroupNotFound}
	svc := newGroupAdminForTest(repo, nil, nil)

	err := svc.DeleteGroup(context.Background(), 99)
	require.ErrorIs(t, err, routing.ErrGroupNotFound)
}

// TestAdminService_DeleteGroup_Error injects a generic cascade failure into
// the deletion fixture. The service returns the same error so callers can
// identify the failed repository operation.
func TestAdminService_DeleteGroup_Error(t *testing.T) {
	deleteErr := errors.New("delete failed")
	repo := &groupRepoStub{deleteErr: deleteErr}
	svc := newGroupAdminForTest(repo, nil, nil)

	err := svc.DeleteGroup(context.Background(), 42)
	require.ErrorIs(t, err, deleteErr)
}

// TestAdminServiceGroupAdvancedSchedulerOverrides exercises sparse pointer
// overrides, omission, explicit clearing, invalid Top-K, and merged weight
// overflow. Captured writes check copied input storage and rejection before
// persistence, including acceptance of all-zero weights.
func TestAdminServiceGroupAdvancedSchedulerOverrides(t *testing.T) {
	t.Run("create deep copies sparse overrides", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)
		overrides := routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled: groupAdvancedSchedulerOverrideTestPointer(false),
			LBTopK:                groupAdvancedSchedulerOverrideTestPointer(3),
		}

		group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "advanced-overrides", RateMultiplier: 1,
			SchedulerType: string(routing.GroupSchedulerTypeAdvanced), AdvancedSchedulerOverrides: overrides,
		})

		require.NoError(t, err)
		require.False(t, *group.AdvancedSchedulerOverrides.StickyWeightedEnabled)
		require.Equal(t, 3, *repo.created.AdvancedSchedulerOverrides.LBTopK)
		*overrides.LBTopK = 99
		require.Equal(t, 3, *repo.created.AdvancedSchedulerOverrides.LBTopK)
	})

	t.Run("update retains omission and clears explicit empty object", func(t *testing.T) {
		existing := &routing.Group{
			ID: 7, Name: "advanced", Status: billing.StatusActive,
			SchedulerType: routing.GroupSchedulerTypeAdvanced,
			AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
				LBTopK: groupAdvancedSchedulerOverrideTestPointer(3),
			},
		}
		repo := &groupRepoStubForAdmin{getByID: existing}
		svc := newGroupAdminForTest(repo, nil, nil)

		unchanged, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{})
		require.NoError(t, err)
		require.Equal(t, 3, *unchanged.AdvancedSchedulerOverrides.LBTopK)

		empty := routing.GroupAdvancedSchedulerOverrides{}
		cleared, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{AdvancedSchedulerOverrides: &empty})
		require.NoError(t, err)
		require.Zero(t, cleared.AdvancedSchedulerOverrides)
		require.Zero(t, repo.updated.AdvancedSchedulerOverrides)
	})

	t.Run("invalid overrides are rejected", func(t *testing.T) {
		svc := newGroupAdminForTest(&groupRepoStubForAdmin{}, nil, nil)
		_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "invalid-advanced-overrides", RateMultiplier: 1,
			AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
				LBTopK: groupAdvancedSchedulerOverrideTestPointer(0),
			},
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, "INVALID_ADVANCED_SCHEDULER_OVERRIDES", apperror.Reason(err))
	})

	t.Run("merged weight overflow is rejected", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		// Choose individually finite weights whose sum overflows float64 so
		// validation checks the merged scheduler configuration before a repository
		// write can be recorded.
		weights := policy.ConfigScoreWeights{Priority: math.MaxFloat64 * 0.75}
		svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, nil, &weights)

		_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "overflowing-advanced-overrides", RateMultiplier: 1,
			AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
				WeightLoad: groupAdvancedSchedulerOverrideTestPointer(math.MaxFloat64 * 0.75),
			},
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, "INVALID_ADVANCED_SCHEDULER_OVERRIDES", apperror.Reason(err))
		require.Nil(t, repo.created)
	})

	t.Run("update rejects merged weight overflow", func(t *testing.T) {
		existing := &routing.Group{
			ID: 8, Name: "existing-advanced-overrides",
			Status: billing.StatusActive, SchedulerType: routing.GroupSchedulerTypeAdvanced,
		}
		repo := &groupRepoStubForAdmin{getByID: existing}
		// Choose individually finite weights whose sum overflows float64 so
		// validation checks the merged scheduler configuration before a repository
		// write can be recorded.
		weights := policy.ConfigScoreWeights{Priority: math.MaxFloat64 * 0.75}
		svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, nil, &weights)
		overrides := routing.GroupAdvancedSchedulerOverrides{
			WeightLoad: groupAdvancedSchedulerOverrideTestPointer(math.MaxFloat64 * 0.75),
		}

		_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
			AdvancedSchedulerOverrides: &overrides,
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, "INVALID_ADVANCED_SCHEDULER_OVERRIDES", apperror.Reason(err))
		require.Nil(t, repo.updated)
	})

	t.Run("all zero base weights remain writable", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)
		zero := 0.0

		_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "zero-base-advanced-overrides", RateMultiplier: 1,
			AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
				WeightPriority:      &zero,
				WeightLoad:          &zero,
				WeightQueue:         &zero,
				WeightErrorRate:     &zero,
				WeightTTFT:          &zero,
				WeightReset:         &zero,
				WeightQuotaHeadroom: &zero,
			},
		})

		require.NoError(t, err)
		require.NotNil(t, repo.created)
	})
}

// TestAdminService_GetGroupModelsListCandidates_UsesConfiguredRequestModels
// supplies explicit model allowlists on two provider platforms. The
// saved-group query records the requested group ID and returns the sorted
// union of eligible request names.
func TestAdminService_GetGroupModelsListCandidates_UsesConfiguredRequestModels(t *testing.T) {
	groupID := int64(10)
	groupRepo := &groupRepoStubForAdmin{
		getByID: &routing.Group{ID: groupID},
	}
	providerRepo := &groupModelsListProviderRepoStub{
		providers: []provider.Record{
			{
				ID:       1,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"model_whitelist": []any{"deepseek-v4-pro", "deepseek-v4-flash"},
				},
			},
			{
				ID:       2,
				Platform: capability.PlatformAnthropic,
				Credentials: map[string]any{
					"model_whitelist": []any{"claude-sonnet-4-6"},
				},
			},
		},
	}
	svc := newGroupAdminPortsForTest(groupRepo, nil, nil, nil, providerRepo, nil, nil)

	models, err := svc.GetGroupModelsListCandidates(context.Background(), groupID, "")

	require.NoError(t, err)
	require.Equal(t, groupID, providerRepo.calledGroupID)
	require.Equal(t, []string{"claude-sonnet-4-6", "deepseek-v4-flash", "deepseek-v4-pro"}, models)
}

// TestGroupModelsListCandidatesApplyGroupMappingAndRestrictions varies
// allowlist checks across requested, group-mapped, and upstream stages with
// explicit provider support. It checks eligible request and target names,
// then limits a custom list to its one supported entry.
func TestGroupModelsListCandidatesApplyGroupMappingAndRestrictions(t *testing.T) {
	for _, stage := range []string{routing.BillingModelSourceRequested, routing.BillingModelSourceGroupMapped, routing.BillingModelSourceUpstream} {
		t.Run(stage, func(t *testing.T) {
			group := &routing.Group{ID: 59, RoutingPolicy: routing.GroupRoutingPolicy{
				Enabled: true, RestrictModels: true, RestrictionModelSource: stage,
				ModelMapping: map[string]string{
					"gemini-3.8-flash":  "gemini-3.8-flash-tiered",
					"gemini-3.7-flash":  "gemini-3.7-flash-tiered",
					"gemini-3.1-pro":    "gemini-3.1-pro-high",
					"unavailable-alias": "missing-model",
				},
			}}
			requested := []string{"gemini-3.1-pro", "gemini-3.7-flash", "gemini-3.8-flash"}
			mapped := []string{"gemini-3.1-pro-high", "gemini-3.7-flash-tiered", "gemini-3.8-flash-tiered"}
			allowed := requested
			if stage != routing.BillingModelSourceRequested {
				allowed = mapped
			}
			group.RoutingPolicy.AllowedModels = append(append([]string{}, allowed...), "unavailable-alias")
			providers := &groupModelsListProviderRepoStub{providers: []provider.Record{{
				ID: 3678, Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{"model_whitelist": mapped},
			}}}
			svc := newGroupAdminPortsForTest(&groupRepoStubForAdmin{getByID: group}, nil, nil, nil, providers, nil, nil)
			models, err := svc.GetGroupModelsListCandidates(context.Background(), group.ID, "")
			require.NoError(t, err)
			if stage == routing.BillingModelSourceRequested {
				require.Equal(t, requested, models)
			} else {
				require.ElementsMatch(t, append(append([]string{}, requested...), mapped...), models)
			}
			// Select one supported request name and an alias whose target has no
			// provider route, so custom-list filtering must retain the supported
			// entry alone.
			group.ModelsListConfig = routing.GroupModelsListConfig{Enabled: true, Models: []string{"gemini-3.8-flash", "unavailable-alias"}}
			models, err = svc.GetGroupModelsListCandidates(context.Background(), group.ID, "")
			require.NoError(t, err)
			require.Equal(t, []string{"gemini-3.8-flash"}, models)
		})
	}
}

// TestAdminServiceCustomModelsCannotInventUnsupportedModels supplies custom
// names outside a provider's nonempty allowlist, then empties that allowlist.
// Candidates are initially empty and become the configured custom names after
// provider admission opens.
func TestAdminServiceCustomModelsCannotInventUnsupportedModels(t *testing.T) {
	groupID := int64(12)
	groupRepo := &groupRepoStubForAdmin{
		getByID: &routing.Group{
			ID: groupID,

			ModelsListConfig: routing.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"deepseek-v4-flash", "deepseek-v4-pro"},
			},
		},
	}
	providerRepo := &groupModelsListProviderRepoStub{
		providers: []provider.Record{
			{ID: 1, Platform: capability.PlatformOpenAI, Credentials: map[string]any{"model_whitelist": []string{"gpt-5.6-sol"}}},
		},
	}
	svc := newGroupAdminPortsForTest(groupRepo, nil, nil, nil, providerRepo, nil, nil)

	models, err := svc.GetGroupModelsListCandidates(context.Background(), groupID, "")

	require.NoError(t, err)
	require.Equal(t, groupID, providerRepo.calledGroupID)
	require.Empty(t, models)
	providerRepo.providers[0].Credentials["model_whitelist"] = []string{}
	models, err = svc.GetGroupModelsListCandidates(context.Background(), groupID, "")
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek-v4-flash", "deepseek-v4-pro"}, models)
}

// TestAdminService_GetGroupModelsListCandidates_FiltersCustomModelsList
// supplies two provider-supported names and a custom list containing one
// unsupported name. The result retains the supported names in custom-list
// order.
func TestAdminService_GetGroupModelsListCandidates_FiltersCustomModelsList(t *testing.T) {
	groupID := int64(13)
	groupRepo := &groupRepoStubForAdmin{
		getByID: &routing.Group{
			ID: groupID,

			ModelsListConfig: routing.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"deepseek-v4-pro", "gpt-5.5", "deepseek-v4-flash"},
			},
		},
	}
	providerRepo := &groupModelsListProviderRepoStub{
		providers: []provider.Record{
			{
				ID:       1,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"model_whitelist": []any{"deepseek-v4-flash", "deepseek-v4-pro"},
				},
			},
		},
	}
	svc := newGroupAdminPortsForTest(groupRepo, nil, nil, nil, providerRepo, nil, nil)

	models, err := svc.GetGroupModelsListCandidates(context.Background(), groupID, "")

	require.NoError(t, err)
	require.Equal(t, []string{"deepseek-v4-pro", "deepseek-v4-flash"}, models)
}

// TestAdminServiceGetGroupModelsListCandidatesKeepsEmptyIntersection supplies
// a DeepSeek-named custom entry with a provider restricted to one Claude name.
// The candidate query succeeds with an empty result.
func TestAdminServiceGetGroupModelsListCandidatesKeepsEmptyIntersection(t *testing.T) {
	groupID := int64(14)
	groupRepo := &groupRepoStubForAdmin{
		getByID: &routing.Group{
			ID: groupID,

			ModelsListConfig: routing.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"deepseek-v4-flash"},
			},
		},
	}
	providerRepo := &groupModelsListProviderRepoStub{
		providers: []provider.Record{
			{
				ID:       1,
				Platform: capability.PlatformAnthropic,
				Credentials: map[string]any{
					"model_whitelist": []any{"claude-sonnet-4-6"},
				},
			},
		},
	}
	svc := newGroupAdminPortsForTest(groupRepo, nil, nil, nil, providerRepo, nil, nil)

	models, err := svc.GetGroupModelsListCandidates(context.Background(), groupID, "")

	require.NoError(t, err)
	require.Empty(t, models)
}

// TestAdminService_GetGroupModelsListCandidates_FallsBackToPlatformDefaults
// supplies an OpenAI provider without explicit model configuration. It
// compares returned candidates with DefaultProviderModels after removing
// embeddings and checks one embedding name is absent.
func TestAdminService_GetGroupModelsListCandidates_FallsBackToPlatformDefaults(t *testing.T) {
	groupID := int64(11)
	groupRepo := &groupRepoStubForAdmin{
		getByID: &routing.Group{ID: groupID},
	}
	providerRepo := &groupModelsListProviderRepoStub{
		providers: []provider.Record{
			{ID: 1, Platform: capability.PlatformOpenAI},
		},
	}
	svc := newGroupAdminPortsForTest(groupRepo, nil, nil, nil, providerRepo, nil, nil)

	models, err := svc.GetGroupModelsListCandidates(context.Background(), groupID, "")

	require.NoError(t, err)
	expected := provideradapter.DefaultProviderModels(&provider.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey})
	expected = slices.DeleteFunc(expected, func(model string) bool { return strings.HasPrefix(model, "text-embedding-") })
	require.ElementsMatch(t, expected, models)
	require.NotContains(t, models, "text-embedding-3-small")
}

// TestAdminService_CreateGroup_AppendsSortOrder supplies a saved tail
// position of forty and omits a new position. Creation records fifty,
// requests the ordering lock once, and queries the descending one-row tail.
func TestAdminService_CreateGroup_AppendsSortOrder(t *testing.T) {
	repo := &groupRepoStubForAdmin{
		listWithFiltersGroups: []routing.Group{{ID: 9, SortOrder: 40}},
	}
	svc := newGroupAdminPortsForTest(repo, nil, nil, repo, nil, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name:           "appended-group",
		RateMultiplier: 1,
	})

	require.NoError(t, err)
	require.Equal(t, 50, group.SortOrder)
	require.Equal(t, 50, repo.created.SortOrder)
	require.Equal(t, 1, repo.groupSortOrderLockCalls)
	require.Equal(t, pagination.PaginationParams{
		Page:      1,
		PageSize:  1,
		SortBy:    "sort_order",
		SortOrder: "desc",
	}, repo.listWithFiltersParams)
}

// TestAdminService_CreateGroup_PreservesExplicitSortOrder supplies a position
// of five during creation. The returned group retains it and the fixture
// records neither an ordering lock nor a tail query.
func TestAdminService_CreateGroup_PreservesExplicitSortOrder(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := newGroupAdminPortsForTest(repo, nil, nil, repo, nil, nil, nil)
	explicitSortOrder := 5

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name:           "explicit-sort-group",
		RateMultiplier: 1,
		SortOrder:      &explicitSortOrder,
	})

	require.NoError(t, err)
	require.Equal(t, explicitSortOrder, group.SortOrder)
	require.Equal(t, 0, repo.groupSortOrderLockCalls)
	require.Equal(t, 0, repo.listWithFiltersCalls)
}

// TestAdminService_UpdateGroup_OpenAIFastInvalidatesAuthCache enables the
// legacy Fast field on a saved group. The captured update enables it and
// records invalidation for that group's authentication snapshot.
func TestAdminService_UpdateGroup_OpenAIFastInvalidatesAuthCache(t *testing.T) {
	existingGroup := &routing.Group{ID: 1, Name: "existing-fast", Status: billing.StatusActive}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	invalidator := &authCacheInvalidatorStub{}
	svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, invalidator, nil)
	enabled := true

	group, err := svc.UpdateGroup(context.Background(), existingGroup.ID, &routing.UpdateGroupInput{
		ForceOpenAIFast: &enabled,
	})

	require.NoError(t, err)
	require.NotNil(t, group)
	require.True(t, repo.updated.ForceOpenAIFast)
	require.Equal(t, []int64{existingGroup.ID}, invalidator.groupIDs)
}

// TestAdminService_UpdateGroup_InvalidatesAuthCacheOnRPMLimitChange replaces
// a saved RPM limit with sixty. The repository receives that limit and the
// invalidator records the updated group ID.
func TestAdminService_UpdateGroup_InvalidatesAuthCacheOnRPMLimitChange(t *testing.T) {
	existingGroup := &routing.Group{
		ID:   1,
		Name: "existing-group",

		Status:   billing.StatusActive,
		RPMLimit: 10,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	invalidator := &authCacheInvalidatorStub{}
	svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, invalidator, nil)

	rpmLimit := 60
	group, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		RPMLimit: &rpmLimit,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Equal(t, 60, repo.updated.RPMLimit)
	require.Equal(t, []int64{1}, invalidator.groupIDs, "分组 RPMLimit 写入 auth snapshot，变更后必须失效 API Key 认证缓存")
}

// TestAdminGroupOpenAIFastPolicy creates and updates each named Fast policy
// alongside the legacy boolean. Assertions check named-policy precedence, the
// priority boolean mirror, omission retention, legacy disabling, and
// authentication invalidation.
func TestAdminGroupOpenAIFastPolicy(t *testing.T) {
	for _, policy := range []string{"follow_request", "force_priority", "force_ultrafast", "force_off"} {
		t.Run(policy, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			invalidator := &authCacheInvalidatorStub{}
			svc := newGroupAdminPortsForTest(repo, nil, nil, nil, nil, invalidator, nil)
			group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "fast", RateMultiplier: 1, ForceOpenAIFast: true, OpenAIFastPolicy: &policy})
			require.NoError(t, err)
			require.Equal(t, policy, group.OpenAIFastPolicy)
			require.Equal(t, policy == "force_priority", group.ForceOpenAIFast)
			group.ID = 1
			repo.getByID = group
			kept, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{})
			require.NoError(t, err)
			require.Equal(t, policy, kept.OpenAIFastPolicy)
			off := false
			changed, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{ForceOpenAIFast: &off})
			require.NoError(t, err)
			require.Equal(t, "follow_request", changed.OpenAIFastPolicy)
			changed, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{OpenAIFastPolicy: &policy})
			require.NoError(t, err)
			require.Equal(t, policy, changed.OpenAIFastPolicy)
			require.Contains(t, invalidator.groupIDs, int64(1))
			changed, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{})
			require.NoError(t, err)
			require.Equal(t, policy, changed.OpenAIFastPolicy)
		})
	}
}

// TestAdminServiceCreateGroupUsesUnifiedClientProtocolDefaults creates a
// group with no protocol selection. It checks the three default text entries,
// empty conversion choices, and disabled image, batch-image, and Live flags.
func TestAdminServiceCreateGroupUsesUnifiedClientProtocolDefaults(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	group, err := newGroupAdminForTest(repo, nil, nil).CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "mixed", RateMultiplier: 1})
	require.NoError(t, err)
	require.Equal(t, []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolOpenAIResponses, protocol.ProtocolOpenAIChatCompletions}, group.AllowedProtocols)
	require.Empty(t, group.ProtocolFallbacks)
	require.False(t, group.AllowImageGeneration)
	require.False(t, group.AllowBatchImageGeneration)
	require.False(t, group.AllowLive)
}

// TestAdminServiceGroupAvailabilityProbeConfigReturnsBadRequest supplies more
// retries than the probe configuration permits to create and update calls.
// Both return the designated HTTP 400 reason before recording a repository
// write.
func TestAdminServiceGroupAvailabilityProbeConfigReturnsBadRequest(t *testing.T) {
	invalidRetries := routing.MaxGroupAvailabilityProbeMaxRetries + 1
	invalidConfig := routing.GroupAvailabilityProbeConfig{
		Enabled:    true,
		ModelID:    "gpt-5.4",
		Prompt:     "hi",
		MaxRetries: &invalidRetries,
	}

	t.Run("create rejects invalid config", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)

		_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "invalid-probe", RateMultiplier: 1,
			AvailabilityProbeConfig: invalidConfig,
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, routing.InvalidGroupAvailabilityProbeConfigReason, apperror.Reason(err))
		require.Nil(t, repo.created)
	})

	t.Run("update rejects invalid config", func(t *testing.T) {
		existing := &routing.Group{ID: 7, Name: "existing", Status: billing.StatusActive}
		repo := &groupRepoStubForAdmin{getByID: existing}
		svc := newGroupAdminForTest(repo, nil, nil)

		_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
			AvailabilityProbeConfig: &invalidConfig,
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, routing.InvalidGroupAvailabilityProbeConfigReason, apperror.Reason(err))
		require.Nil(t, repo.updated)
	})
}

// TestAdminServiceGroupSchedulerTypeDefaultsValidatesAndUpdates exercises
// omitted, advanced, and invalid scheduler values, plus an explicit update.
// It checks stored defaults and choices and the HTTP 400 reason for an
// unsupported type.
func TestAdminServiceGroupSchedulerTypeDefaultsValidatesAndUpdates(t *testing.T) {
	t.Run("create defaults to basic", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)

		group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "default-scheduler", RateMultiplier: 1,
		})

		require.NoError(t, err)
		require.Equal(t, routing.GroupSchedulerTypeBasic, group.SchedulerType)
		require.Equal(t, routing.GroupSchedulerTypeBasic, repo.created.SchedulerType)
	})

	t.Run("create accepts advanced", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)

		group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "advanced-scheduler", RateMultiplier: 1, SchedulerType: string(routing.GroupSchedulerTypeAdvanced),
		})

		require.NoError(t, err)
		require.Equal(t, routing.GroupSchedulerTypeAdvanced, group.SchedulerType)
	})

	t.Run("invalid value is rejected", func(t *testing.T) {
		svc := newGroupAdminForTest(&groupRepoStubForAdmin{}, nil, nil)

		_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "invalid-scheduler", RateMultiplier: 1, SchedulerType: "weighted",
		})

		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Equal(t, "INVALID_SCHEDULER_TYPE", apperror.Reason(err))
	})

	t.Run("update preserves explicit advanced choice", func(t *testing.T) {
		existing := &routing.Group{ID: 7, Name: "basic", Status: billing.StatusActive, SchedulerType: routing.GroupSchedulerTypeBasic}
		repo := &groupRepoStubForAdmin{getByID: existing}
		svc := newGroupAdminForTest(repo, nil, nil)
		advanced := string(routing.GroupSchedulerTypeAdvanced)

		group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{SchedulerType: &advanced})

		require.NoError(t, err)
		require.Equal(t, routing.GroupSchedulerTypeAdvanced, group.SchedulerType)
		require.Equal(t, routing.GroupSchedulerTypeAdvanced, repo.updated.SchedulerType)
	})
}

// TestAdminServiceCreateGroupClientProtocolCompatibilityPrecedence supplies
// the legacy Messages switch with omitted or explicit protocol entries.
// Assertions check the default selection and the explicit selection's derived
// compatibility boolean.
func TestAdminServiceCreateGroupClientProtocolCompatibilityPrecedence(t *testing.T) {
	t.Run("legacy OpenAI switch is accepted when new field is omitted", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)

		group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "legacy", RateMultiplier: 1, AllowMessagesDispatch: true,
		})

		require.NoError(t, err)
		require.Equal(t, []protocol.ProtocolID{
			protocol.ProtocolAnthropicMessages,
			protocol.ProtocolOpenAIResponses,
			protocol.ProtocolOpenAIChatCompletions,
		}, group.AllowedProtocols)
		require.True(t, group.AllowMessagesDispatch)
	})

	t.Run("new field wins over legacy OpenAI switch", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{}
		svc := newGroupAdminForTest(repo, nil, nil)

		group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
			Name: "new-field",

			RateMultiplier:        1,
			AllowMessagesDispatch: true,
			AllowedProtocols: []protocol.ProtocolID{
				protocol.ProtocolOpenAIChatCompletions,
				protocol.ProtocolOpenAIResponses,
			},
		})

		require.NoError(t, err)
		require.Equal(t, []protocol.ProtocolID{
			protocol.ProtocolOpenAIResponses,
			protocol.ProtocolOpenAIChatCompletions,
		}, group.AllowedProtocols)
		require.False(t, group.AllowMessagesDispatch)
	})
}

// TestAdminServiceRejectsInvalidGroupClientProtocols supplies unknown and
// repeated client protocol IDs during creation. Each call returns HTTP 400
// with INVALID_ALLOWED_CLIENT_PROTOCOLS for that invalid selection.
func TestAdminServiceRejectsInvalidGroupClientProtocols(t *testing.T) {
	tests := []struct {
		name      string                // invalid selection case label
		platform  string                // unused historical platform label
		protocols []protocol.ProtocolID // supplied client protocol selection
	}{
		{"unknown", capability.PlatformQoder, []protocol.ProtocolID{"unknown"}},
		{"duplicate", capability.PlatformQoder, []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolAnthropicMessages}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newGroupAdminForTest(&groupRepoStubForAdmin{}, nil, nil)
			_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
				Name: tt.name, RateMultiplier: 1, AllowedProtocols: tt.protocols,
			})

			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
			require.Equal(t, "INVALID_ALLOWED_CLIENT_PROTOCOLS", apperror.Reason(err))
		})
	}
}

// TestAdminServiceAllowsEmptyGroupClientProtocolsForEveryPlatform repeats
// creation under several platform-named test labels while supplying the same
// empty selection. Every returned group retains a nonnil empty protocol
// slice.
func TestAdminServiceAllowsEmptyGroupClientProtocolsForEveryPlatform(t *testing.T) {
	platforms := []string{
		capability.PlatformAnthropic,
		capability.PlatformOpenAI,
		capability.PlatformGemini,
		capability.PlatformAntigravity,
		capability.PlatformQoder,
		capability.PlatformGrok,
	}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			svc := newGroupAdminForTest(&groupRepoStubForAdmin{}, nil, nil)

			group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
				Name: platform, RateMultiplier: 1, AllowedProtocols: []protocol.ProtocolID{},
			})

			require.NoError(t, err)
			require.NotNil(t, group.AllowedProtocols)
			require.Empty(t, group.AllowedProtocols)
		})
	}
}

// TestAdminServiceUpdateGroupPreservesExplicitEmptyClientProtocols updates a
// group whose saved protocol selection is explicitly empty. The resulting
// slice stays nonnil and empty when the update omits protocols.
func TestAdminServiceUpdateGroupPreservesExplicitEmptyClientProtocols(t *testing.T) {
	existing := &routing.Group{ID: 1, Name: "openai", Status: billing.StatusActive, AllowedProtocols: []protocol.ProtocolID{}}
	repo := &groupRepoStubForAdmin{getByID: existing}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{})

	require.NoError(t, err)
	require.NotNil(t, group.AllowedProtocols)
	require.Empty(t, group.AllowedProtocols)
}

// TestAdminServiceUpdateGroupPreservesAllConfiguredProtocols renames a group
// with text and batch entries already configured. The entire protocol
// selection remains equal and the batch compatibility flag stays enabled.
func TestAdminServiceUpdateGroupPreservesAllConfiguredProtocols(t *testing.T) {
	protocols := []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolOpenAIResponses, protocol.ProtocolGeminiGenerateContent, protocol.ProtocolImageBatches}
	repo := &groupRepoStubForAdmin{getByID: &routing.Group{ID: 1, Name: "before", Status: billing.StatusActive, AllowedProtocols: protocols}}
	group, err := newGroupAdminForTest(repo, nil, nil).UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{Name: "after"})
	require.NoError(t, err)
	require.Equal(t, protocols, group.AllowedProtocols)
	require.True(t, group.AllowBatchImageGeneration)
}

// TestAdminServiceUpdateGroupNewClientProtocolsOverrideLegacySwitch supplies
// an explicit Messages-capable selection together with a false legacy switch.
// The resulting group permits Messages and derives an enabled compatibility
// flag.
func TestAdminServiceUpdateGroupNewClientProtocolsOverrideLegacySwitch(t *testing.T) {
	existing := &routing.Group{
		ID: 1, Name: "openai", Status: billing.StatusActive,
		AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses, protocol.ProtocolOpenAIChatCompletions},
	}
	repo := &groupRepoStubForAdmin{getByID: existing}
	svc := newGroupAdminForTest(repo, nil, nil)
	legacyEnabled := false

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		AllowedProtocols: ptrGroupClientProtocols([]protocol.ProtocolID{
			protocol.ProtocolAnthropicMessages,
			protocol.ProtocolOpenAIResponses,
			protocol.ProtocolOpenAIChatCompletions,
		}),
		AllowMessagesDispatch: &legacyEnabled,
	})

	require.NoError(t, err)
	require.True(t, group.AllowMessagesDispatch)
	require.True(t, group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages))
}

// TestAdminService_ListGroups_PassesSortParams supplies page three and size
// twenty-five with provider-count ordering and an uppercase direction. The
// fixture captures the supplied pagination and sort values exactly.
func TestAdminService_ListGroups_PassesSortParams(t *testing.T) {
	repo := &groupRepoStubForAdmin{
		listWithFiltersGroups: []routing.Group{{ID: 1, Name: "g1"}},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, _, err := svc.ListGroups(context.Background(), 3, 25, capability.PlatformOpenAI, billing.StatusActive, "needle", nil, "provider_count", "ASC")
	require.NoError(t, err)
	require.Equal(t, pagination.PaginationParams{
		Page:      3,
		PageSize:  25,
		SortBy:    "provider_count",
		SortOrder: "ASC",
	}, repo.listWithFiltersParams)
}

// TestAdminService_ListGroups_PassesSessionIsolationSortParams requests
// descending session-isolation ordering on page one with size twenty. The
// captured pagination object retains the supplied field, direction, page, and
// size.
func TestAdminService_ListGroups_PassesSessionIsolationSortParams(t *testing.T) {
	repo := &groupRepoStubForAdmin{
		listWithFiltersGroups: []routing.Group{{ID: 1, Name: "g1"}},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, _, err := svc.ListGroups(context.Background(), 1, 20, "", "", "", nil, "session_isolation_enabled", "DESC")
	require.NoError(t, err)
	require.Equal(t, pagination.PaginationParams{
		Page:      1,
		PageSize:  20,
		SortBy:    "session_isolation_enabled",
		SortOrder: "DESC",
	}, repo.listWithFiltersParams)
}

// TestAdminService_CreateGroup_PreservesNonGrokImageGenerationDisabled
// creates a text-labeled group with media fields omitted. Both the returned
// group and captured creation value keep image generation disabled.
func TestAdminService_CreateGroup_PreservesNonGrokImageGenerationDisabled(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name:        "anthropic-text",
		Description: "Anthropic text group",

		RateMultiplier: 1.0,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.False(t, repo.created.AllowImageGeneration)
	require.False(t, group.AllowImageGeneration)
}

// TestAdminService_CreateGroup_WithSessionIsolation supplies session
// isolation during creation. Both the returned group and the repository's
// captured creation argument retain the enabled value.
func TestAdminService_CreateGroup_WithSessionIsolation(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "isolated-group",

		RateMultiplier:          1.0,
		SessionIsolationEnabled: true,
	})

	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.True(t, repo.created.SessionIsolationEnabled)
	require.True(t, group.SessionIsolationEnabled)
}

// TestAdminService_CreateGroup_DisablesBatchImageWhenImageGenerationDisabled
// requests the legacy batch flag while image generation is false. Captured
// and returned values keep image and batch generation disabled.
func TestAdminService_CreateGroup_DisablesBatchImageWhenImageGenerationDisabled(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name:        "gemini-no-image",
		Description: "Gemini group without image generation",

		RateMultiplier:            1.0,
		AllowImageGeneration:      false,
		AllowBatchImageGeneration: true,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)

	require.False(t, repo.created.AllowImageGeneration)
	require.False(t, repo.created.AllowBatchImageGeneration)
	require.False(t, group.AllowBatchImageGeneration)
}

// TestAdminServiceCreateGroupAllowsExplicitBatchProtocol creates a group with
// the image-batches client protocol selected directly. The returned group's
// batch compatibility flag is enabled independently of legacy media inputs.
func TestAdminServiceCreateGroupAllowsExplicitBatchProtocol(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	group, err := newGroupAdminForTest(repo, nil, nil).CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "batch", RateMultiplier: 1, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolImageBatches}})
	require.NoError(t, err)
	require.True(t, group.AllowBatchImageGeneration)
}

// TestAdminServiceCreateGroupPreservesFastPolicies creates a group with the
// legacy ForceOpenAIFast switch enabled. The returned group retains an
// enabled Fast boolean after administrative normalization.
func TestAdminServiceCreateGroupPreservesFastPolicies(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	group, err := newGroupAdminForTest(repo, nil, nil).CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "fast", RateMultiplier: 1, ForceOpenAIFast: true})
	require.NoError(t, err)
	require.True(t, group.ForceOpenAIFast)
}

// TestAdminServiceUpdateGroupPreservesFastPolicies supplies a saved enabled
// legacy Fast field and omits Fast updates. The repository's captured update
// retains the enabled value.
func TestAdminServiceUpdateGroupPreservesFastPolicies(t *testing.T) {
	existingGroup := &routing.Group{
		ID: 1, Name: "existing-fast", Status: billing.StatusActive,
		ForceOpenAIFast: true,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.UpdateGroup(context.Background(), existingGroup.ID, &routing.UpdateGroupInput{})

	require.NoError(t, err)
	require.NotNil(t, group)
	require.True(t, repo.updated.ForceOpenAIFast)
}

// TestAdminService_UpdateGroup_PreservesImageGenerationControlsWhenOmitted
// edits a description on a group admitting image generation and editing. The
// captured update retains image generation when its update field is absent.
func TestAdminService_UpdateGroup_PreservesImageGenerationControlsWhenOmitted(t *testing.T) {
	existingGroup := &routing.Group{
		ID:   1,
		Name: "existing-group",

		Status:               billing.StatusActive,
		AllowImageGeneration: true,
		AllowedProtocols:     []protocol.ProtocolID{"openai_images_generations", "openai_images_edits"},
		ProtocolFallbacks:    map[protocol.ProtocolID][]protocol.ProtocolID{},
		ResponsesImagePolicy: "inherit",
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)

	updatedDesc := "updated"
	group, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		Description: &updatedDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.True(t, repo.updated.AllowImageGeneration)
}

// TestAdminService_UpdateGroup_WithSessionIsolation enables session isolation
// on a saved group. Both the returned value and the captured repository
// update contain the enabled setting.
func TestAdminService_UpdateGroup_WithSessionIsolation(t *testing.T) {
	existingGroup := &routing.Group{
		ID:   1,
		Name: "existing-group",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)
	enabled := true

	group, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		SessionIsolationEnabled: &enabled,
	})

	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.True(t, repo.updated.SessionIsolationEnabled)
	require.True(t, group.SessionIsolationEnabled)
}

// TestAdminService_UpdateGroup_DisablesBatchImageWhenImageGenerationDisabled
// turns off legacy image generation on a group with batch generation enabled.
// The captured update and returned group also disable batching.
func TestAdminService_UpdateGroup_DisablesBatchImageWhenImageGenerationDisabled(t *testing.T) {
	existingGroup := &routing.Group{
		ID:   1,
		Name: "existing-gemini",

		Status:                    billing.StatusActive,
		AllowImageGeneration:      true,
		AllowBatchImageGeneration: true,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)
	disabled := false

	group, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		AllowImageGeneration: &disabled,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.False(t, repo.updated.AllowImageGeneration)
	require.False(t, repo.updated.AllowBatchImageGeneration)
	require.False(t, group.AllowBatchImageGeneration)
}

// TestAdminService_UpdateGroup_ClearsDescriptionWhenEmptyString supplies a
// pointer to an empty description for a saved nonempty value. The captured
// repository update contains the empty string.
func TestAdminService_UpdateGroup_ClearsDescriptionWhenEmptyString(t *testing.T) {
	existingGroup := &routing.Group{
		ID:          1,
		Name:        "existing-group",
		Description: "Auto-created default group",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)

	empty := ""
	_, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		Description: &empty,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, "", repo.updated.Description, "空字符串应清空分组描述")
}

// TestAdminService_UpdateGroup_PreservesDescriptionWhenNil omits the
// description by supplying nil for a saved nonempty value. The repository's
// captured update retains the existing text.
func TestAdminService_UpdateGroup_PreservesDescriptionWhenNil(t *testing.T) {
	existingGroup := &routing.Group{
		ID:          1,
		Name:        "existing-group",
		Description: "keep me",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForAdmin{getByID: existingGroup}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
		Description: nil,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, "keep me", repo.updated.Description, "nil 应保留原有分组描述")
}

// TestAdminService_UpdateGroup_ReasoningEffortMappingsTriState varies
// omitted, empty, replacement, and model-scoped effort mappings. The captured
// updates distinguish retention from clearing and normalize effort, model,
// and match-type spelling.
func TestAdminService_UpdateGroup_ReasoningEffortMappingsTriState(t *testing.T) {
	tests := []struct {
		name  string                           // effort update case label
		input *routing.UpdateGroupInput        // supplied group update fields
		want  []routing.ReasoningEffortMapping // expected normalized effort mappings
	}{
		{
			name:  "nil preserves existing mappings",
			input: &routing.UpdateGroupInput{},
			want:  []routing.ReasoningEffortMapping{{From: "max", To: "xhigh"}},
		},
		{
			name: "empty array clears mappings",
			input: func() *routing.UpdateGroupInput {
				empty := []routing.ReasoningEffortMapping{}
				return &routing.UpdateGroupInput{ReasoningEffortMappings: &empty}
			}(),
			want: []routing.ReasoningEffortMapping{},
		},
		{
			name: "non empty array replaces and canonicalizes mappings",
			input: func() *routing.UpdateGroupInput {
				replacement := []routing.ReasoningEffortMapping{{From: " X-HIGH ", To: " high "}}
				return &routing.UpdateGroupInput{ReasoningEffortMappings: &replacement}
			}(),
			want: []routing.ReasoningEffortMapping{{From: "xhigh", To: "high"}},
		},
		{
			name: "model scoped mappings are canonicalized independently",
			input: func() *routing.UpdateGroupInput {
				replacement := []routing.ReasoningEffortMapping{
					{From: " MAX ", To: " low ", MatchType: "PREFIX", Model: " gpt "},
					{From: "max", To: "medium", Model: "gpt-5.4"},
				}
				return &routing.UpdateGroupInput{ReasoningEffortMappings: &replacement}
			}(),
			want: []routing.ReasoningEffortMapping{
				{From: "max", To: "low", MatchType: "prefix", Model: "gpt"},
				{From: "max", To: "medium", MatchType: "exact", Model: "gpt-5.4"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := &routing.Group{
				ID:   1,
				Name: "openai-group",

				Status:                  billing.StatusActive,
				ReasoningEffortMappings: []routing.ReasoningEffortMapping{{From: "max", To: "xhigh"}},
			}
			repo := &groupRepoStubForAdmin{getByID: existing}
			svc := newGroupAdminForTest(repo, nil, nil)

			_, err := svc.UpdateGroup(context.Background(), existing.ID, tt.input)

			require.NoError(t, err)
			require.Equal(t, tt.want, repo.updated.ReasoningEffortMappings)
		})
	}
}

// TestAdminService_UpdateGroup_RejectsInvalidReasoningEffortMappings supplies
// two mapping sources that collide after effort normalization. It checks the
// duplicate-source error and that no update is recorded.
func TestAdminService_UpdateGroup_RejectsInvalidReasoningEffortMappings(t *testing.T) {
	existing := &routing.Group{
		ID:   1,
		Name: "openai",

		RateMultiplier: 1,
		Status:         billing.StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{groups: map[int64]*routing.Group{existing.ID: existing}}
	svc := newGroupAdminForTest(repo, nil, nil)
	invalid := []routing.ReasoningEffortMapping{
		{From: "max", To: "xhigh"},
		{From: " MAX ", To: "high"},
	}

	_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		ReasoningEffortMappings: &invalid,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate reasoning effort mapping source")
	require.Nil(t, repo.updated)
}

// TestAdminServiceUpdateGroupPreservesReasoningPolicy omits effort settings
// on a group with a ceiling, denial mode, and mapping. The captured update
// retains those selected policy values.
func TestAdminServiceUpdateGroupPreservesReasoningPolicy(t *testing.T) {
	existing := &routing.Group{
		ID:   1,
		Name: "openai-group",

		Status:                      billing.StatusActive,
		MaxReasoningEffort:          "medium",
		MaxReasoningEffortOverLimit: routing.ReasoningEffortOverLimitDeny,
		ReasoningEffortMappings:     []routing.ReasoningEffortMapping{{From: "max", To: "xhigh"}},
	}
	repo := &groupRepoStubForAdmin{getByID: existing}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{})

	require.NoError(t, err)
	require.Equal(t, "medium", repo.updated.MaxReasoningEffort)
	require.Equal(t, routing.ReasoningEffortOverLimitDeny, repo.updated.MaxReasoningEffortOverLimit)
	require.Equal(t, existing.ReasoningEffortMappings, repo.updated.ReasoningEffortMappings)
}

// TestAdminServiceUpdateGroupPreservesMessagesDispatchPolicy renames a group
// already admitting Messages and Live with a default mapped model. The
// returned group retains both derived flags and that model.
func TestAdminServiceUpdateGroupPreservesMessagesDispatchPolicy(t *testing.T) {
	existing := &routing.Group{ID: 1, Name: "mixed", Status: billing.StatusActive, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolLive}, DefaultMappedModel: "gpt-test"}
	repo := &groupRepoStubForAdmin{getByID: existing}
	group, err := newGroupAdminForTest(repo, nil, nil).UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{Name: "renamed"})
	require.NoError(t, err)
	require.True(t, group.AllowMessagesDispatch)
	require.True(t, group.AllowLive)
	require.Equal(t, "gpt-test", group.DefaultMappedModel)
}

// TestAdminService_ListGroups_WithSearch exercises populated, empty, and
// combined search filters with supplied pagination metadata. Assertions check
// returned rows and totals and the repository's captured page and filter
// inputs.
func TestAdminService_ListGroups_WithSearch(t *testing.T) {
	t.Run("search 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{
			listWithFiltersGroups: []routing.Group{{ID: 1, Name: "alpha"}},
			listWithFiltersResult: &pagination.PaginationResult{Total: 1},
		}
		svc := newGroupAdminForTest(repo, nil, nil)

		groups, total, err := svc.ListGroups(context.Background(), 1, 20, "", "", "alpha", nil, "", "")
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		require.Equal(t, []routing.Group{{ID: 1, Name: "alpha"}}, groups)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 1, PageSize: 20}, repo.listWithFiltersParams)
		require.Equal(t, "alpha", repo.listWithFiltersSearch)
		require.Nil(t, repo.listWithFiltersIsExclusive)
	})

	t.Run("search 为空字符串时传递空字符串", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{
			listWithFiltersGroups: []routing.Group{},
			listWithFiltersResult: &pagination.PaginationResult{Total: 0},
		}
		svc := newGroupAdminForTest(repo, nil, nil)

		groups, total, err := svc.ListGroups(context.Background(), 2, 10, "", "", "", nil, "", "")
		require.NoError(t, err)
		require.Empty(t, groups)
		require.Equal(t, int64(0), total)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 2, PageSize: 10}, repo.listWithFiltersParams)
		require.Equal(t, "", repo.listWithFiltersSearch)
		require.Nil(t, repo.listWithFiltersIsExclusive)
	})

	t.Run("search 与其他过滤条件组合使用", func(t *testing.T) {
		isExclusive := true
		repo := &groupRepoStubForAdmin{
			listWithFiltersGroups: []routing.Group{{ID: 2, Name: "beta"}},
			listWithFiltersResult: &pagination.PaginationResult{Total: 42},
		}
		svc := newGroupAdminForTest(repo, nil, nil)

		groups, total, err := svc.ListGroups(context.Background(), 3, 50, capability.PlatformAntigravity, billing.StatusActive, "beta", &isExclusive, "", "")
		require.NoError(t, err)
		require.Equal(t, int64(42), total)
		require.Equal(t, []routing.Group{{ID: 2, Name: "beta"}}, groups)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 3, PageSize: 50}, repo.listWithFiltersParams)
		require.Equal(t, capability.PlatformAntigravity, repo.listWithFiltersPlatform)
		require.Equal(t, billing.StatusActive, repo.listWithFiltersStatus)
		require.Equal(t, "beta", repo.listWithFiltersSearch)
		require.NotNil(t, repo.listWithFiltersIsExclusive)
		require.True(t, *repo.listWithFiltersIsExclusive)
	})
}

// TestAdminService_ValidateFallbackGroup_DetectsCycle configures two groups
// whose client-fallback references point at each other. Validation returns an
// error identifying the repeated fallback chain.
func TestAdminService_ValidateFallbackGroup_DetectsCycle(t *testing.T) {
	groupID := int64(1)
	fallbackID := int64(2)
	repo := &groupRepoStubForFallbackCycle{
		groups: map[int64]*routing.Group{
			groupID: {
				ID:              groupID,
				FallbackGroupID: &fallbackID,
			},
			fallbackID: {
				ID:              fallbackID,
				FallbackGroupID: &groupID,
			},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	err := svc.ValidateFallbackGroup(context.Background(), groupID, fallbackID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "fallback group cycle")
}

// TestAdminService_CreateGroup_InvalidRequestFallbackRejectsFallbackGroup
// supplies a target that already has an invalid-request fallback. Creation
// returns the nesting error and records no group write.
func TestAdminService_CreateGroup_InvalidRequestFallbackRejectsFallbackGroup(t *testing.T) {
	tests := []struct {
		name        string         // invalid target case label
		fallback    *routing.Group // configured fallback target group
		wantMessage string         // expected validation message fragment
	}{
		{
			name: "nested_fallback",
			fallback: &routing.Group{
				ID: 10,

				FallbackGroupIDOnInvalidRequest: func() *int64 { v := int64(99); return &v }(),
			},
			wantMessage: "fallback group cannot have invalid request fallback configured",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fallbackID := tc.fallback.ID
			repo := &groupRepoStubForInvalidRequestFallback{
				groups: map[int64]*routing.Group{
					fallbackID: tc.fallback,
				},
			}
			svc := newGroupAdminForTest(repo, nil, nil)

			_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
				Name: "g1",

				RateMultiplier:                  1.0,
				FallbackGroupIDOnInvalidRequest: &fallbackID,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantMessage)
			require.Nil(t, repo.created)
		})
	}
}

// TestAdminService_CreateGroup_InvalidRequestFallbackNotFound supplies a
// positive target ID absent from the fixture. Creation reports the missing
// fallback group and records no group write.
func TestAdminService_CreateGroup_InvalidRequestFallbackNotFound(t *testing.T) {
	fallbackID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "g1",

		RateMultiplier:                  1.0,
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "fallback group not found")
	require.Nil(t, repo.created)
}

// TestAdminService_CreateGroup_InvalidRequestFallbackAllowsAnthropic creates
// an invalid-request fallback using a bare target record. The captured
// creation retains its ID; the request supplies no platform field.
func TestAdminService_CreateGroup_InvalidRequestFallbackAllowsAnthropic(t *testing.T) {
	fallbackID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			fallbackID: {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "g1",

		RateMultiplier:                  1.0,
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.Equal(t, fallbackID, *repo.created.FallbackGroupIDOnInvalidRequest)
}

// TestAdminService_CreateGroup_InvalidRequestFallbackAllowsAntigravity
// creates an invalid-request fallback using a bare target record. The
// captured creation retains its ID; the request supplies no platform field.
func TestAdminService_CreateGroup_InvalidRequestFallbackAllowsAntigravity(t *testing.T) {
	fallbackID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			fallbackID: {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "g1",

		RateMultiplier:                  1.0,
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.Equal(t, fallbackID, *repo.created.FallbackGroupIDOnInvalidRequest)
}

// TestAdminService_CreateGroup_InvalidRequestFallbackClearsOnZero supplies a
// zero target pointer during creation. The operation succeeds and the
// captured group stores a nil invalid-request fallback.
func TestAdminService_CreateGroup_InvalidRequestFallbackClearsOnZero(t *testing.T) {
	zero := int64(0)
	repo := &groupRepoStubForInvalidRequestFallback{}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "g1",

		RateMultiplier:                  1.0,
		FallbackGroupIDOnInvalidRequest: &zero,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.Nil(t, repo.created.FallbackGroupIDOnInvalidRequest)
}

// TestAdminService_CreateGroup_UnavailableFallbackAllowsSamePlatformActiveGroup
// supplies an active target for unavailable-group fallback. Creation retains
// its ID in the captured value; the request supplies no platform field.
func TestAdminService_CreateGroup_UnavailableFallbackAllowsSamePlatformActiveGroup(t *testing.T) {
	fallbackID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			fallbackID: {ID: fallbackID, Status: billing.StatusActive},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
		Name: "g1",

		RateMultiplier:             1.0,
		UnavailableFallbackGroupID: &fallbackID,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.created)
	require.Equal(t, fallbackID, *repo.created.UnavailableFallbackGroupID)
}

// TestAdminService_CreateGroup_UnavailableFallbackRejectsInvalidGroup
// supplies a target whose status is disabled for unavailable-group fallback.
// Creation returns the activity error and records no repository write.
func TestAdminService_CreateGroup_UnavailableFallbackRejectsInvalidGroup(t *testing.T) {
	tests := []struct {
		name        string         // inactive target case label
		fallback    *routing.Group // configured fallback target group
		wantMessage string         // expected validation message fragment
	}{
		{
			name:        "inactive_target",
			fallback:    &routing.Group{ID: 10, Status: billing.StatusDisabled},
			wantMessage: "unavailable fallback group must be active",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fallbackID := tc.fallback.ID
			repo := &groupRepoStubForInvalidRequestFallback{
				groups: map[int64]*routing.Group{
					fallbackID: tc.fallback,
				},
			}
			svc := newGroupAdminForTest(repo, nil, nil)

			_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
				Name: "g1",

				RateMultiplier:             1.0,
				UnavailableFallbackGroupID: &fallbackID,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantMessage)
			require.Nil(t, repo.created)
		})
	}
}

// TestAdminService_UpdateGroup_UnavailableFallbackRejectsSelf supplies the
// destination's own ID as its unavailable fallback. The update reports the
// self-reference error and records no repository write.
func TestAdminService_UpdateGroup_UnavailableFallbackRejectsSelf(t *testing.T) {
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{existing.ID: existing},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		UnavailableFallbackGroupID: &existing.ID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot set self as unavailable fallback group")
	require.Nil(t, repo.updated)
}

// TestAdminService_UpdateGroup_UnavailableFallbackClearsOnZero replaces a
// saved positive target with a supplied pointer to zero. The successful
// captured update stores a nil unavailable fallback.
func TestAdminService_UpdateGroup_UnavailableFallbackClearsOnZero(t *testing.T) {
	fallbackID := int64(10)
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status:                     billing.StatusActive,
		UnavailableFallbackGroupID: &fallbackID,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			existing.ID: existing,
			fallbackID:  {ID: fallbackID, Status: billing.StatusActive},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	clear := int64(0)
	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		UnavailableFallbackGroupID: &clear,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Nil(t, repo.updated.UnavailableFallbackGroupID)
}

// TestAdminService_UpdateGroup_InvalidRequestFallbackClearsOnZero replaces a
// saved invalid-request target with a zero pointer. The update succeeds and
// its captured repository argument clears that target.
func TestAdminService_UpdateGroup_InvalidRequestFallbackClearsOnZero(t *testing.T) {
	fallbackID := int64(10)
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status:                          billing.StatusActive,
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			existing.ID: existing,
			fallbackID:  {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	clear := int64(0)
	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		FallbackGroupIDOnInvalidRequest: &clear,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Nil(t, repo.updated.FallbackGroupIDOnInvalidRequest)
}

// TestAdminServiceUpdateGroupAllowsConfiguredFallback supplies an existing
// bare target as an invalid-request fallback during update. The operation
// succeeds and the captured update stores its ID.
func TestAdminServiceUpdateGroupAllowsConfiguredFallback(t *testing.T) {
	fallbackID := int64(10)
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			existing.ID: existing,
			fallbackID:  {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	_, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.NoError(t, err)
	require.Equal(t, fallbackID, *repo.updated.FallbackGroupIDOnInvalidRequest)
}

// TestAdminService_UpdateGroup_InvalidRequestFallbackSetSuccess supplies a
// target with no nested invalid-request fallback. The service returns a group
// and the captured update retains that target ID.
func TestAdminService_UpdateGroup_InvalidRequestFallbackSetSuccess(t *testing.T) {
	fallbackID := int64(10)
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			existing.ID: existing,
			fallbackID:  {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, fallbackID, *repo.updated.FallbackGroupIDOnInvalidRequest)
}

// TestAdminService_UpdateGroup_InvalidRequestFallbackAllowsAntigravity
// supplies an existing bare target during update. The captured value retains
// the invalid-request fallback ID; the request supplies no platform field.
func TestAdminService_UpdateGroup_InvalidRequestFallbackAllowsAntigravity(t *testing.T) {
	fallbackID := int64(10)
	existing := &routing.Group{
		ID:   1,
		Name: "g1",

		Status: billing.StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*routing.Group{
			existing.ID: existing,
			fallbackID:  {ID: fallbackID},
		},
	}
	svc := newGroupAdminForTest(repo, nil, nil)

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &routing.UpdateGroupInput{
		FallbackGroupIDOnInvalidRequest: &fallbackID,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, fallbackID, *repo.updated.FallbackGroupIDOnInvalidRequest)
}

// TestUpdateGroupDoesNotInvalidateIndependentPricingCache renames a group
// while installing a pricing-cache spy. The returned name changes and that
// spy records no pricing invalidation.
func TestUpdateGroupDoesNotInvalidateIndependentPricingCache(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &routing.Group{ID: 7, Name: "before"}}
	spy := &pricingConfigCacheInvalidatorSpy{}
	svc := newGroupAdminForTest(repo, nil, spy)
	got, err := svc.UpdateGroup(context.Background(), 7, &routing.UpdateGroupInput{Name: "after"})
	require.NoError(t, err)
	require.Equal(t, "after", got.Name)
	require.Zero(t, spy.calls)
}

// TestUpdateGroupWithoutPricingConfigCacheInvalidator updates a group with an
// empty input while supplying no pricing invalidator. The call succeeds and
// returns the fixture's existing group name.
func TestUpdateGroupWithoutPricingConfigCacheInvalidator(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &routing.Group{ID: 7, Name: "g"}}
	svc := newGroupAdminForTest(repo, nil, nil)

	got, err := svc.UpdateGroup(context.Background(), 7, &routing.UpdateGroupInput{})
	require.NoError(t, err)
	require.Equal(t, "g", got.Name)
}

// TestUpdateGroupDoesNotInvalidatePricingConfigCacheWhenUpdateFails injects a
// repository update error alongside a pricing-cache spy. The service returns
// an error and nil group while the spy count remains zero.
func TestUpdateGroupDoesNotInvalidatePricingConfigCacheWhenUpdateFails(t *testing.T) {
	repo := &groupPlatformRepoStub{
		group:     &routing.Group{ID: 7, Name: "g"},
		updateErr: errors.New("update failed"),
	}
	spy := &pricingConfigCacheInvalidatorSpy{}
	svc := newGroupAdminForTest(repo, nil, spy)

	got, err := svc.UpdateGroup(context.Background(), 7, &routing.UpdateGroupInput{})
	require.Error(t, err)
	require.Nil(t, got)
	require.Zero(t, spy.calls)
}

// TestProtocolGroupPersistenceAndCacheIsolation creates text and image-edit
// admission, copies it through an authentication snapshot, and applies legacy
// then explicit-empty updates. Assertions check copied fallback storage,
// retained media distinctions, clearing, and rejection of an incompatible
// conversion.
func TestProtocolGroupPersistenceAndCacheIsolation(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := newGroupAdminForTest(repo, nil, nil)
	created, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "protocol", RateMultiplier: 1, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolImagesEdits}, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolAnthropicMessages: {protocol.ProtocolOpenAIResponses}}, ResponsesImagePolicy: "disabled"})
	require.NoError(t, err)
	require.False(t, created.AllowsClientProtocol(protocol.ProtocolOpenAIResponses))
	require.True(t, created.AllowImageGeneration)
	repo.getByID = created
	created.ID = 1
	snapshot := apikey.KeyAuthGroupSnapshotFromGroup(created)
	restored := apikey.KeyGroupFromAuthSnapshot(snapshot)
	// Change the reconstructed fallback target before inspecting the snapshot,
	// making aliasing of the nested protocol slice observable at the
	// authentication-copy boundary.
	restored.ProtocolFallbacks[protocol.ProtocolAnthropicMessages][0] = protocol.ProtocolOpenAIChatCompletions
	require.Equal(t, []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}, snapshot.ProtocolFallbacks[protocol.ProtocolAnthropicMessages])
	require.Equal(t, "disabled", restored.ResponsesImagePolicy)
	protocols := []protocol.ProtocolID{protocol.ProtocolOpenAIChatCompletions}
	updated, err := svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{AllowedProtocols: &protocols, LegacyProtocolInput: true})
	require.NoError(t, err)
	require.Contains(t, updated.AllowedProtocols, protocol.ProtocolImagesEdits)
	require.NotContains(t, updated.AllowedProtocols, protocol.ProtocolImagesGenerations)
	updated, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{Name: "renamed"})
	require.NoError(t, err)
	require.NotContains(t, updated.AllowedProtocols, protocol.ProtocolImagesGenerations)
	empty := []protocol.ProtocolID{}
	updated, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{AllowedProtocols: &empty, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{}, ResponsesImagePolicy: "block"})
	require.NoError(t, err)
	require.Empty(t, updated.AllowedProtocols)
	require.Empty(t, updated.ProtocolFallbacks)
	_, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolEmbeddings: {protocol.ProtocolOpenAIResponses}}})
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
}

// TestGroupProtocolLegacyPatchDoesNotHideInvalidInput combines legacy image
// input with unknown or repeated protocol IDs in create and update requests.
// Each invalid selection returns the protocol HTTP 400 reason before a write
// is recorded.
func TestGroupProtocolLegacyPatchDoesNotHideInvalidInput(t *testing.T) {
	for _, protocols := range [][]protocol.ProtocolID{
		{"unknown"},
		{protocol.ProtocolImagesEdits, protocol.ProtocolImagesEdits},
	} {
		t.Run(string(protocols[0]), func(t *testing.T) {
			enabled := true
			repo := &groupRepoStubForAdmin{}
			svc := newGroupAdminForTest(repo, nil, nil)
			_, err := svc.CreateGroup(context.Background(), &routing.CreateGroupInput{
				Name: "invalid", RateMultiplier: 1,
				AllowedProtocols: protocols, LegacyProtocolInput: true, AllowImageGeneration: enabled,
			})
			require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
			require.Equal(t, "INVALID_ALLOWED_CLIENT_PROTOCOLS", apperror.Reason(err))
			require.Nil(t, repo.created)
			repo.getByID = &routing.Group{ID: 1, RateMultiplier: 1, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}}
			_, err = svc.UpdateGroup(context.Background(), 1, &routing.UpdateGroupInput{
				AllowedProtocols: &protocols, LegacyProtocolInput: true, AllowImageGeneration: &enabled,
			})
			require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
			require.Equal(t, "INVALID_ALLOWED_CLIENT_PROTOCOLS", apperror.Reason(err))
			require.Nil(t, repo.updated)
		})
	}
}

// Create panics when deletion tests request a new group insertion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) Create(ctx context.Context, group *routing.Group) error {
	panic("unexpected Create call")
}

// GetByID panics when deletion tests request a full group read. The fixture
// has no implementation for this repository path, so invoking it exposes an
// unexpected dependency call immediately.
func (s *groupRepoStub) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	panic("unexpected GetByID call")
}

// GetByIDLite panics when deletion tests request a lightweight group read.
// The fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	panic("unexpected GetByIDLite call")
}

// Update panics when deletion tests request a group update. The fixture has
// no implementation for this repository path, so invoking it exposes an
// unexpected dependency call immediately.
func (s *groupRepoStub) Update(ctx context.Context, group *routing.Group) error {
	panic("unexpected Update call")
}

// Delete panics when deletion tests request direct group deletion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

// DeleteCascade records the requested group ID before returning its
// configured user-ID slice and error. The result slice remains shared with
// the fixture, including on an injected failure.
func (s *groupRepoStub) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	s.deleteCalls = append(s.deleteCalls, id)
	return s.affectedUserIDs, s.deleteErr
}

// List panics when deletion tests request an unfiltered group page. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics when deletion tests request a filtered group page.
// The fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// ListActive panics when deletion tests request all active groups. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStub) ListActive(ctx context.Context) ([]routing.Group, error) {
	panic("unexpected ListActive call")
}

// ListActiveByPlatform panics when deletion tests request platform-filtered
// active groups. The fixture has no implementation for this repository path,
// so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStub) ListActiveByPlatform(ctx context.Context, platform string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

// ListActiveByPlatformLite panics when deletion tests request lightweight
// platform-filtered active groups. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStub) ListActiveByPlatformLite(ctx context.Context, platform string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

// ExistsByName panics when deletion tests request a group-name existence
// check. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStub) ExistsByName(ctx context.Context, name string) (bool, error) {
	panic("unexpected ExistsByName call")
}

// GetProviderCount panics when deletion tests request separate provider count
// hydration. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStub) GetProviderCount(ctx context.Context, groupID int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

// DeleteProviderGroupsByGroupID panics when deletion tests request
// provider-membership cleanup. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStub) DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

// BindProvidersToGroup panics when deletion tests request provider-membership
// binding. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStub) BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

// GetProviderIDsByGroupIDs panics when deletion tests request source provider
// memberships. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStub) GetProviderIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

// UpdateSortOrders accepts sort writes successfully and stores no ordering
// arguments.
func (s *groupRepoStub) UpdateSortOrders(ctx context.Context, updates []routing.GroupSortOrderUpdate) error {
	return nil
}

// ListKeysByGroupID records the requested group before returning the
// configured keys. A supplied listErr returns nil keys with that error;
// successful key storage remains shared with the fixture.
func (s *deleteGroupAPIKeyRepoStub) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	s.listGroupIDs = append(s.listGroupIDs, groupID)
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.keys, nil
}

// ListSchedulableByGroupID records the group ID and converts an outer-slice
// copy of its provider records into catalogue providers. Blank credential
// types become API-key types; nested record data remains borrowed during
// conversion.
func (s *groupModelsListProviderRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]routing.CatalogueProvider, error) {
	s.calledGroupID = groupID
	records := append([]provider.Record(nil), s.providers...)
	for i := range records {
		// Supply API-key type for blank fixture records so catalogue resolution
		// evaluates ordinary API-key protocol and model eligibility while leaving
		// the source records' scalar fields untouched.
		if records[i].Type == "" {
			records[i].Type = capability.ProviderTypeAPIKey
		}
	}
	return gatewayprovider.CatalogueProviders(records), nil
}

// groupAdvancedSchedulerOverrideTestPointer allocates a supplied optional
// override value for each test call.
func groupAdvancedSchedulerOverrideTestPointer[T any](value T) *T { return &value }

// InvalidateAuthCacheByGroupID records each group snapshot invalidation
// requested by administration.
func (s *authCacheInvalidatorStub) InvalidateAuthCacheByGroupID(_ context.Context, id int64) {
	s.groupIDs = append(s.groupIDs, id)
}

// InvalidateAuthCacheByKey records each credential string selected for
// snapshot invalidation.
func (s *authCacheInvalidatorStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}

// ptrGroupClientProtocols returns a pointer to a copy of the slice header.
// The header preserves explicit empty input, while the referenced protocol
// backing array remains shared with value.
func ptrGroupClientProtocols(value []protocol.ProtocolID) *[]protocol.ProtocolID {
	return &value
}

// Create panics when fallback-cycle tests request a new group insertion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) Create(_ context.Context, _ *routing.Group) error {
	panic("unexpected Create call")
}

// Update panics when fallback-cycle tests request a group update. The fixture
// has no implementation for this repository path, so invoking it exposes an
// unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) Update(_ context.Context, _ *routing.Group) error {
	panic("unexpected Update call")
}

// GetByID delegates cycle-test reads to GetByIDLite with the supplied context
// and ID. Returned group pointers remain attached to the fixture's configured
// map for chain traversal.
func (s *groupRepoStubForFallbackCycle) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	return s.GetByIDLite(ctx, id)
}

// GetByIDLite returns a configured group pointer when the ID exists in the
// map. An absent ID returns ErrGroupNotFound, exposing a missing link during
// fallback-chain validation.
func (s *groupRepoStubForFallbackCycle) GetByIDLite(_ context.Context, id int64) (*routing.Group, error) {
	if g, ok := s.groups[id]; ok {
		return g, nil
	}
	return nil, routing.ErrGroupNotFound
}

// Delete panics when fallback-cycle tests request direct group deletion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) Delete(_ context.Context, _ int64) error {
	panic("unexpected Delete call")
}

// DeleteCascade panics when fallback-cycle tests request cascade group
// deletion. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

// List panics when fallback-cycle tests request an unfiltered group page. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) List(_ context.Context, _ pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics when fallback-cycle tests request a filtered group
// page. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _ string, _ *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// ListActive panics when fallback-cycle tests request all active groups. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) ListActive(_ context.Context) ([]routing.Group, error) {
	panic("unexpected ListActive call")
}

// ListActiveByPlatform panics when fallback-cycle tests request
// platform-filtered active groups. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForFallbackCycle) ListActiveByPlatform(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

// ListActiveByPlatformLite panics when fallback-cycle tests request
// lightweight platform-filtered active groups. The fixture has no
// implementation for this repository path, so invoking it exposes an
// unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) ListActiveByPlatformLite(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

// ExistsByName panics when fallback-cycle tests request a group-name
// existence check. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) ExistsByName(_ context.Context, _ string) (bool, error) {
	panic("unexpected ExistsByName call")
}

// GetProviderCount panics when fallback-cycle tests request separate provider
// count hydration. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) GetProviderCount(_ context.Context, _ int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

// DeleteProviderGroupsByGroupID panics when fallback-cycle tests request
// provider-membership cleanup. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForFallbackCycle) DeleteProviderGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

// BindProvidersToGroup panics when fallback-cycle tests request
// provider-membership binding. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForFallbackCycle) BindProvidersToGroup(_ context.Context, _ int64, _ []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

// GetProviderIDsByGroupIDs panics when fallback-cycle tests request source
// provider memberships. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForFallbackCycle) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

// UpdateSortOrders accepts ordering requests successfully and keeps the cycle
// fixture unchanged.
func (s *groupRepoStubForFallbackCycle) UpdateSortOrders(_ context.Context, _ []routing.GroupSortOrderUpdate) error {
	return nil
}

// Create retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured creation argument.
func (s *groupRepoStubForInvalidRequestFallback) Create(_ context.Context, g *routing.Group) error {
	s.created = g
	return nil
}

// Update retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured update argument.
func (s *groupRepoStubForInvalidRequestFallback) Update(_ context.Context, g *routing.Group) error {
	s.updated = g
	return nil
}

// GetByID delegates to GetByIDLite with the supplied context and ID. The
// shared configured group pointer or missing-ID error supports the fallback
// tests' destination and target reads.
func (s *groupRepoStubForInvalidRequestFallback) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	return s.GetByIDLite(ctx, id)
}

// GetByIDLite returns the retained group pointer for an existing map key. An
// absent key returns ErrGroupNotFound, allowing a missing fallback target to
// fail validation before any write.
func (s *groupRepoStubForInvalidRequestFallback) GetByIDLite(_ context.Context, id int64) (*routing.Group, error) {
	if g, ok := s.groups[id]; ok {
		return g, nil
	}
	return nil, routing.ErrGroupNotFound
}

// Delete panics when invalid-request fallback tests request direct group
// deletion. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForInvalidRequestFallback) Delete(_ context.Context, _ int64) error {
	panic("unexpected Delete call")
}

// DeleteCascade panics when invalid-request fallback tests request cascade
// group deletion. The fixture has no implementation for this repository path,
// so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForInvalidRequestFallback) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

// List panics when invalid-request fallback tests request an unfiltered group
// page. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForInvalidRequestFallback) List(_ context.Context, _ pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters accepts the exact descending one-row query used by
// automatic sort allocation and panics for other query shapes. It selects the
// greatest SortOrder from the configured map, returning a value copy and
// pagination whose total is the map size. An empty map returns nil rows and a
// zero-total page.
//
// Equal sort positions keep whichever group map traversal encounters first.
// Nested references remain shared in the returned group value, and the
// fixture supplies no ordering lock.
func (s *groupRepoStubForInvalidRequestFallback) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	if params.Page != 1 || params.PageSize != 1 || params.SortBy != "sort_order" || params.SortOrder != "desc" || platform != "" || status != "" || search != "" || isExclusive != nil {
		panic("unexpected ListWithFilters call")
	}
	// Find the maximum position for NextGroupSortOrder to increment. Equal
	// maxima produce the same allocation even when map traversal retains a
	// different group identity.
	var last *routing.Group
	for _, group := range s.groups {
		if last == nil || group.SortOrder > last.SortOrder {
			last = group
		}
	}
	if last == nil {
		return nil, &pagination.PaginationResult{Page: 1, PageSize: 1}, nil
	}
	return []routing.Group{*last}, &pagination.PaginationResult{Total: int64(len(s.groups)), Page: 1, PageSize: 1}, nil
}

// ListActive panics when invalid-request fallback tests request all active
// groups. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForInvalidRequestFallback) ListActive(_ context.Context) ([]routing.Group, error) {
	panic("unexpected ListActive call")
}

// ListActiveByPlatform panics when invalid-request fallback tests request
// platform-filtered active groups. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) ListActiveByPlatform(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

// ListActiveByPlatformLite panics when invalid-request fallback tests request
// lightweight platform-filtered active groups. The fixture has no
// implementation for this repository path, so invoking it exposes an
// unexpected dependency call immediately.
func (s *groupRepoStubForInvalidRequestFallback) ListActiveByPlatformLite(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

// ExistsByName panics when invalid-request fallback tests request a
// group-name existence check. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) ExistsByName(_ context.Context, _ string) (bool, error) {
	panic("unexpected ExistsByName call")
}

// GetProviderCount panics when invalid-request fallback tests request
// separate provider count hydration. The fixture has no implementation for
// this repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) GetProviderCount(_ context.Context, _ int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

// DeleteProviderGroupsByGroupID panics when invalid-request fallback tests
// request provider-membership cleanup. The fixture has no implementation for
// this repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) DeleteProviderGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

// GetProviderIDsByGroupIDs panics when invalid-request fallback tests request
// source provider memberships. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

// BindProvidersToGroup panics when invalid-request fallback tests request
// provider-membership binding. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForInvalidRequestFallback) BindProvidersToGroup(_ context.Context, _ int64, _ []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

// UpdateSortOrders accepts sort requests successfully and stores no updated
// positions.
func (s *groupRepoStubForInvalidRequestFallback) UpdateSortOrders(_ context.Context, _ []routing.GroupSortOrderUpdate) error {
	return nil
}

// GetByID returns a shallow copy of the configured nonnil group and ignores
// the requested ID. Scalar edits are separated, while nested references
// remain attached to the fixture.
func (r *groupPlatformRepoStub) GetByID(_ context.Context, _ int64) (*routing.Group, error) {
	cloned := *r.group
	return &cloned, nil
}

// Update returns the configured error before recording a group when updateErr
// is set. Otherwise it retains the supplied group pointer and returns success
// for inspection by the test.
func (r *groupPlatformRepoStub) Update(_ context.Context, group *routing.Group) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	r.updated = group
	return nil
}

// InvalidateCache increments the observed pricing-cache request count for
// administrative assertions.
func (s *pricingConfigCacheInvalidatorSpy) InvalidateCache() { s.calls++ }

// TestAdminServiceProbeProtocolValidatesMergedGroup removes an entry from an existing probe group.
// Updating only group protocols must reject the resulting mismatch, while disabling the probe permits the same update.
func TestAdminServiceProbeProtocolValidatesMergedGroup(t *testing.T) {
	for _, disable := range []bool{false, true} {
		existing := &routing.Group{
			ID: 7, Name: "probe", Status: billing.StatusActive,
			AllowedProtocols:        []protocol.ProtocolID{protocol.ProtocolOpenAIResponses},
			AvailabilityProbeConfig: routing.GroupAvailabilityProbeConfig{Enabled: true, ModelID: "alias", Prompt: "hi", Protocol: protocol.ProtocolOpenAIResponses},
		}
		repo := &groupRepoStubForAdmin{getByID: existing}
		input := &routing.UpdateGroupInput{AllowedProtocols: ptrGroupClientProtocols([]protocol.ProtocolID{protocol.ProtocolAnthropicMessages})}
		if disable {
			input.AvailabilityProbeConfig = &routing.GroupAvailabilityProbeConfig{Enabled: false}
		}
		_, err := newGroupAdminForTest(repo, nil, nil).UpdateGroup(t.Context(), existing.ID, input)
		if disable {
			require.NoError(t, err)
		} else {
			require.Equal(t, routing.InvalidGroupAvailabilityProbeConfigReason, apperror.Reason(err))
			require.Nil(t, repo.updated)
		}
	}
}
