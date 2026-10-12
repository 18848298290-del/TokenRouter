package routing

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

type mockPricingConfigRepository struct {
	readGroup                        func(context.Context, int64) (*Group, error)
	listAllFn                        func(ctx context.Context) ([]PricingConfig, error)
	getGroupPlatformsFn              func(ctx context.Context, groupIDs []int64) (map[int64]string, error)
	createFn                         func(ctx context.Context, pricingConfig *PricingConfig) error
	getByIDFn                        func(ctx context.Context, id int64) (*PricingConfig, error)
	updateFn                         func(ctx context.Context, pricingConfig *PricingConfig) error
	deleteFn                         func(ctx context.Context, id int64) error
	listFn                           func(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PricingConfig, *pagination.PaginationResult, error)
	existsByNameFn                   func(ctx context.Context, name string) (bool, error)
	existsByNameExcludingFn          func(ctx context.Context, name string, excludeID int64) (bool, error)
	getGroupIDsFn                    func(ctx context.Context, pricingConfigID int64) ([]int64, error)
	setGroupIDsFn                    func(ctx context.Context, pricingConfigID int64, groupIDs []int64) error
	getPricingConfigIDByGroupIDFn    func(ctx context.Context, groupID int64) (int64, error)
	getGroupsInOtherPricingConfigsFn func(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error)
	listModelPricingFn               func(ctx context.Context, pricingConfigID int64) ([]ModelPricingEntry, error)
	createModelPricingFn             func(ctx context.Context, pricing *ModelPricingEntry) error
	updateModelPricingFn             func(ctx context.Context, pricing *ModelPricingEntry) error
	deleteModelPricingFn             func(ctx context.Context, id int64) error
	replaceModelPricingFn            func(ctx context.Context, pricingConfigID int64, pricingList []ModelPricingEntry) error
}

type mockPricingConfigAuthCacheInvalidator struct {
	invalidatedGroupIDs []int64
	invalidatedKeys     []string
	invalidatedUserIDs  []int64
}

// TestPricingConfigPublicationOwnsSnapshot checks that later input mutations cannot change cached prices or group
// associations.
func TestPricingConfigPublicationOwnsSnapshot(t *testing.T) {
	pricingConfigs := []PricingConfig{{ID: 1, Status: StatusActive, GroupIDs: []int64{9}}}

	cache := populatePricingConfigCache(pricingConfigs)
	pricingConfigs[0].Status = "disabled"

	require.Equal(t, StatusActive, cache.byID[1].Status)
	require.Equal(t, int64(1), cache.pricingConfigByGroupID[9].ID)
}

func TestBuildModelMappingChain(t *testing.T) {
	tests := []struct {
		name          string
		result        GroupMappingResult
		requestModel  string
		upstreamModel string
		want          string
	}{
		{
			name:          "no mapping, no upstream diff",
			result:        GroupMappingResult{Mapped: false, MappedModel: "claude-sonnet-4"},
			requestModel:  "claude-sonnet-4",
			upstreamModel: "claude-sonnet-4",
			want:          "",
		},
		{
			name:          "no mapping, upstream differs",
			result:        GroupMappingResult{Mapped: false, MappedModel: "claude-sonnet-4"},
			requestModel:  "claude-sonnet-4",
			upstreamModel: "claude-sonnet-4-20250514",
			want:          "claude-sonnet-4\u2192claude-sonnet-4-20250514",
		},
		{
			name:          "mapped, upstream differs",
			result:        GroupMappingResult{Mapped: true, MappedModel: "claude-sonnet-4-20250514"},
			requestModel:  "my-model",
			upstreamModel: "actual-upstream",
			want:          "my-model\u2192claude-sonnet-4-20250514\u2192actual-upstream",
		},
		{
			name:          "mapped, upstream same as mapped",
			result:        GroupMappingResult{Mapped: true, MappedModel: "claude-sonnet-4-20250514"},
			requestModel:  "claude-sonnet-4",
			upstreamModel: "claude-sonnet-4-20250514",
			want:          "claude-sonnet-4\u2192claude-sonnet-4-20250514",
		},
		{
			name:          "mapped, upstream empty",
			result:        GroupMappingResult{Mapped: true, MappedModel: "target-model"},
			requestModel:  "my-model",
			upstreamModel: "",
			want:          "my-model\u2192target-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.BuildModelMappingChain(tt.requestModel, tt.upstreamModel)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestValidateNoConflictingModels(t *testing.T) {
	tests := []struct {
		name        string
		pricingList []ModelPricingEntry
		wantErr     bool
		errContains string
	}{
		{
			name: "no duplicates",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-sonnet-4", "claude-opus-4"}},
				{Models: []string{"gpt-5.1"}},
			},
			wantErr: false,
		},
		{
			name: "same platform duplicate",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-sonnet-4"}},
				{Models: []string{"claude-sonnet-4"}},
			},
			wantErr:     true,
			errContains: "claude-sonnet-4",
		},
		{
			name: "same model duplicate in unified space",
			pricingList: []ModelPricingEntry{
				{Models: []string{"model-a"}},
				{Models: []string{"model-a"}},
			},
			wantErr: true,
		},
		{
			name: "case insensitive",
			pricingList: []ModelPricingEntry{
				{Models: []string{"Claude"}},
				{Models: []string{"claude"}},
			},
			wantErr: true,
		},
		{
			name:        "empty list (nil)",
			pricingList: nil,
			wantErr:     false,
		},
		{
			name: "wildcard_vs_wildcard_conflict",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-*"}},
				{Models: []string{"claude-opus-*"}},
			},
			wantErr:     true,
			errContains: "conflict",
		},
		{
			name: "wildcard_vs_exact_conflict",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-*"}},
				{Models: []string{"claude-opus-4-6"}},
			},
			wantErr:     true,
			errContains: "conflict",
		},
		{
			name: "overlapping wildcard in unified space",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-opus-*"}},
				{Models: []string{"claude-*"}},
			},
			wantErr: true,
		},
		{
			name: "no_conflict_same_platform_different_prefix",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-opus-*"}},
				{Models: []string{"gpt-*"}},
			},
			wantErr: false,
		},
		{
			name: "catch_all_wildcard_conflicts_with_everything",
			pricingList: []ModelPricingEntry{
				{Models: []string{"*"}},
				{Models: []string{"gpt-5"}},
			},
			wantErr:     true,
			errContains: "conflict",
		},
		// Conflict detection uses the cache's model identity rules.
		// Distinct punctuation remains distinct, while normalization collisions are rejected.
		{
			name: "claude_dot_and_hyphen_spelling_conflict",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-sonnet-4.5"}},
				{Models: []string{"claude-sonnet-4-5"}},
			},
			wantErr:     false,
			errContains: "conflict",
		},
		{
			name: "claude_dot_and_hyphen_spelling_conflict_wildcard",
			pricingList: []ModelPricingEntry{
				{Models: []string{"claude-sonnet-4.5*"}},
				{Models: []string{"claude-sonnet-4-5-x"}},
			},
			wantErr:     false,
			errContains: "conflict",
		},
		{
			name: "surrounding_whitespace_conflict",
			pricingList: []ModelPricingEntry{
				{Models: []string{"gpt-5.6"}},
				{Models: []string{" gpt-5.6 "}},
			},
			wantErr:     true,
			errContains: "conflict",
		},
		{
			// GPT model punctuation also remains part of model identity.
			name: "non_claude_dot_spelling_is_not_normalized",
			pricingList: []ModelPricingEntry{
				{Models: []string{"gpt-5.6"}},
				{Models: []string{"gpt-5-6"}},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoConflictingModels(tt.pricingList)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}

	// Additional sub-case: explicit empty slice
	t.Run("empty list (empty slice)", func(t *testing.T) {
		err := validateNoConflictingModels([]ModelPricingEntry{})
		require.NoError(t, err)
	})
}

// TestValidateNoConflictingMappings rejects overlapping exact and wildcard source patterns.
func TestValidateNoConflictingMappings(t *testing.T) {
	tests := []struct {
		name     string
		mapping  map[string]string
		conflict bool
	}{
		{"nil", nil, false},
		{"separate families", map[string]string{"claude-*": "a", "gpt-*": "b"}, false},
		{"overlapping wildcard", map[string]string{"claude-*": "a", "claude-opus-*": "b"}, true},
		{"exact inside wildcard", map[string]string{"gpt-*": "a", "gpt-4o": "b"}, true},
		{"case duplicate", map[string]string{"GPT-4": "a", "gpt-4": "b"}, true},
		{"dot and hyphen remain distinct", map[string]string{"claude-4.5": "a", "claude-4-5": "b"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoConflictingMappings(tt.mapping)
			if tt.conflict {
				require.ErrorContains(t, err, "conflict")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConflictsBetween(t *testing.T) {
	tests := []struct {
		name string
		a, b modelEntry
		want bool
	}{
		{
			name: "exact same",
			a:    modelEntry{prefix: "claude-opus-4", wildcard: false},
			b:    modelEntry{prefix: "claude-opus-4", wildcard: false},
			want: true,
		},
		{
			name: "exact different",
			a:    modelEntry{prefix: "claude-opus-4", wildcard: false},
			b:    modelEntry{prefix: "gpt-4o", wildcard: false},
			want: false,
		},
		{
			name: "wildcard matches exact",
			a:    modelEntry{prefix: "claude-", wildcard: true},
			b:    modelEntry{prefix: "claude-opus-4", wildcard: false},
			want: true,
		},
		{
			name: "exact does not match unrelated wildcard",
			a:    modelEntry{prefix: "gpt-4o", wildcard: false},
			b:    modelEntry{prefix: "claude-", wildcard: true},
			want: false,
		},
		{
			name: "wildcard prefix overlap",
			a:    modelEntry{prefix: "claude-", wildcard: true},
			b:    modelEntry{prefix: "claude-opus-", wildcard: true},
			want: true,
		},
		{
			name: "wildcards no overlap",
			a:    modelEntry{prefix: "claude-", wildcard: true},
			b:    modelEntry{prefix: "gpt-", wildcard: true},
			want: false,
		},
		{
			name: "catch-all wildcard vs any",
			a:    modelEntry{prefix: "", wildcard: true},
			b:    modelEntry{prefix: "anything", wildcard: false},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, conflictsBetween(tt.a, tt.b))
		})
	}
}

func TestGetPricingConfigForGroup_Success(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Name:     "test-price-config",
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.ID)
	require.Equal(t, "test-price-config", result.Name)

	// returned value should be a clone
	result.Name = "mutated"
	result2, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, "test-price-config", result2.Name)
}

func TestGetPricingConfigForGroup_InactivePricingConfig(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusDisabled,
		GroupIDs: []int64{10},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err)
	require.Nil(t, result)
}

func TestGetPricingConfigForGroup_NoPricingConfig(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result, err := svc.GetPricingConfigForGroup(context.Background(), 999)
	require.NoError(t, err)
	require.Nil(t, result)
}

func TestGetPricingConfigForGroup_CacheError(t *testing.T) {
	repo := &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, errors.New("db connection failed")
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "db connection failed")
}

func TestGetConfigModelPricing_ExactMatch(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)
	require.InDelta(t, 15e-6, *result.InputPrice, 1e-12)
}

func TestGetConfigModelPricing_CaseInsensitive(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "Claude-Opus-4")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)
}

func TestGetConfigModelPricing_NormalizesDotsAndHyphens(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4.8"}, BillingMode: BillingModePerRequest, PerRequestPrice: testPtrFloat64(0.007)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	require.Nil(t, svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4-8"))
	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4.8")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)
	require.Equal(t, BillingModePerRequest, result.BillingMode)
	require.InDelta(t, 0.007, *result.PerRequestPrice, 1e-12)

	require.Nil(t, svc.GetEffectiveConfigModelPricing(context.Background(), 10, "claude-opus-4-8"))
	effective := svc.GetEffectiveConfigModelPricing(context.Background(), 10, "claude-opus-4.8")
	require.NotNil(t, effective)
	require.Equal(t, int64(100), effective.ID)
	require.Equal(t, BillingModePerRequest, effective.BillingMode)
	require.InDelta(t, 0.007, *effective.PerRequestPrice, 1e-12)
}

func TestGetConfigModelPricing_WildcardMatch(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 200, Models: []string{"claude-*"}, InputPrice: testPtrFloat64(10e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-sonnet-4")
	require.NotNil(t, result)
	require.Equal(t, int64(200), result.ID)
}

func TestGetConfigModelPricing_WildcardFirstMatch(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 200, Models: []string{"claude-*"}, InputPrice: testPtrFloat64(10e-6)},
			{ID: 300, Models: []string{"claude-sonnet-*"}, InputPrice: testPtrFloat64(5e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-sonnet-4-20250514")
	require.NotNil(t, result)
	// "claude-*" is defined first, so it matches first regardless of prefix length
	require.Equal(t, int64(200), result.ID)
	require.InDelta(t, 10e-6, *result.InputPrice, 1e-12)
}

func TestGetConfigModelPricing_NoMatch(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "gpt-5.1")
	require.Nil(t, result)
}

func TestGetConfigModelPricing_InactivePricingConfig(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusDisabled,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.Nil(t, result)
}

func TestGetConfigModelPricing_UnifiedModelSpace(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10, 20},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"gpt-5.1"}, InputPrice: testPtrFloat64(5e-6)},
			{ID: 200, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic", 20: "openai"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "gpt-5.1")
	require.NotNil(t, result)

	result = svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)
	require.Equal(t, int64(200), result.ID)

	result = svc.GetConfigModelPricing(context.Background(), 20, "gpt-5.1")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)

	result = svc.GetConfigModelPricing(context.Background(), 20, "claude-opus-4")
	require.NotNil(t, result)
}

func TestGetConfigModelPricing_ReturnsCopy(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)

	// Mutate the returned model slice to test isolation from the cache.
	result.Models = append(result.Models, "hacked")
	result.ID = 999

	// A later lookup must return the cached model list unchanged.
	result2 := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result2)
	require.Equal(t, 1, len(result2.Models))
	require.Equal(t, int64(100), result2.ID)
}

func TestResolveGroupMapping_NoGroupPolicy(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	// Group 999 has no associated pricing configuration.
	result := svc.ResolveGroupMapping(context.Background(), 999, "claude-opus-4")
	require.Equal(t, "claude-opus-4", result.MappedModel)
	require.False(t, result.Mapped)
	require.Equal(t, int64(0), result.PricingConfigID)
}

func TestResolveGroupMapping_ExactMapping(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-sonnet-4": "claude-sonnet-4-20250514"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-sonnet-4")
	require.True(t, result.Mapped)
	require.Equal(t, "claude-sonnet-4-20250514", result.MappedModel)
	require.Equal(t, int64(1), result.PricingConfigID)
}

func TestResolveGroupMapping_WildcardMapping(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"*": "gpt-5.4"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "any-model-name")
	require.True(t, result.Mapped)
	require.Equal(t, "gpt-5.4", result.MappedModel)
}

func TestResolveGroupMapping_WildcardFirstMatch(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-*": "target2", "claude-sonnet-*": "target1"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-sonnet-4")
	require.True(t, result.Mapped)
	// This assertion checks that an overlapping wildcard resolves to a configured target.

	require.Contains(t, []string{"target1", "target2"}, result.MappedModel)
}

func TestResolveGroupMapping_NoMapping(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-sonnet-4": "mapped"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-opus-4")
	require.False(t, result.Mapped)
	require.Equal(t, "claude-opus-4", result.MappedModel)
	require.Equal(t, int64(1), result.PricingConfigID)
}

func TestResolveGroupMapping_DefaultBillingModelSource(t *testing.T) {
	ch := PricingConfig{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		BillingModelSource: "", // empty
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-opus-4")
	require.Equal(t, BillingModelSourceGroupMapped, result.BillingModelSource)
}

func TestResolveGroupMapping_UpstreamBillingModelSource(t *testing.T) {
	ch := PricingConfig{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		BillingModelSource: BillingModelSourceUpstream,
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-opus-4")
	require.Equal(t, BillingModelSourceUpstream, result.BillingModelSource)
}

func TestResolveGroupMapping_DisabledGroupPolicy(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-sonnet-4": "mapped"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusDisabled,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-sonnet-4")
	require.False(t, result.Mapped)
	require.Equal(t, "claude-sonnet-4", result.MappedModel)
	require.Equal(t, int64(0), result.PricingConfigID) // No associated pricing configuration.
}

func TestIsModelRestricted_NoPricingConfig(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	// Group 999 has no associated group policy.
	restricted := svc.IsModelRestricted(context.Background(), 999, "claude-opus-4")
	require.False(t, restricted)
}

func TestIsModelRestricted_RestrictDisabled(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: false}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-opus-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	// Even though model is not in pricing, RestrictModels=false
	restricted := svc.IsModelRestricted(context.Background(), 10, "nonexistent-model")
	require.False(t, restricted)
}

func TestIsModelRestricted_InactivePricingConfig(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusDisabled,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "any-model")
	require.False(t, restricted)
}

func TestIsModelRestricted_ModelInPricing(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-opus-4", "claude-sonnet-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "claude-opus-4")
	require.False(t, restricted)
}

func TestIsModelRestricted_QoderBlankPricingIsAllowlist(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	zero := 0.0
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"qmodel"}, BillingMode: BillingModeToken},
			{Models: []string{"free-model"}, BillingMode: BillingModeToken, InputPrice: &zero},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformQoder})
	svc := newTestPricingConfigService(repo)

	require.False(t, svc.IsModelRestricted(context.Background(), 10, "qmodel"))
	require.False(t, svc.IsModelRestricted(context.Background(), 10, "free-model"))
}

func TestIsModelRestricted_QoderBlankWildcardDoesNotMaskEffectiveWildcard(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	price := 1e-6
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"qwen3.*"}, BillingMode: BillingModeToken},
			{Models: []string{"qwen3.7-*"}, BillingMode: BillingModeToken, InputPrice: &price},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformQoder})
	svc := newTestPricingConfigService(repo)

	require.False(t, svc.IsModelRestricted(context.Background(), 10, "qwen3.7-plus"))
	require.False(t, svc.IsModelRestricted(context.Background(), 10, "qwen3.6-plus"))
}

func TestIsModelRestricted_ModelInWildcard(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-*"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "claude-sonnet-4")
	require.False(t, restricted)
}

func TestIsModelRestricted_ModelNotFound(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-opus-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "gpt-5.1")
	require.True(t, restricted)
}

func TestIsModelRestricted_CaseInsensitive(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-opus-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "Claude-Opus-4")
	require.False(t, restricted)
}

func TestResolveGroupMapping_WithPolicy(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-sonnet-4": "claude-sonnet-4-20250514"}, RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-sonnet-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	gid := int64(10)
	mapping := svc.ResolveGroupMapping(context.Background(), gid, "claude-sonnet-4")
	require.True(t, mapping.RestrictModels)
	require.True(t, mapping.Mapped)
	require.Equal(t, "claude-sonnet-4-20250514", mapping.MappedModel)
}

func TestResolveGroupMapping_UnmappedPolicy(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},

		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-sonnet-4"}},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	gid := int64(10)
	mapping := svc.ResolveGroupMapping(context.Background(), gid, "unknown-model")
	require.True(t, mapping.RestrictModels)
	require.False(t, mapping.Mapped)
	require.Equal(t, "unknown-model", mapping.MappedModel)
}

func TestBuildCache_DBError(t *testing.T) {
	callCount := 0
	repo := &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			callCount++
			return nil, errors.New("database down")
		},
	}
	svc := newTestPricingConfigService(repo)

	// First call should fail
	_, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database down")
	require.Equal(t, 1, callCount)

	// The short-lived error snapshot is empty; the next lookup returns nil without another error.
	result, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err)
	require.Nil(t, result)
	// Should NOT have hit DB again (error-TTL cache is active)
	require.Equal(t, 1, callCount)
}

func TestBuildCacheDoesNotReadRetiredGroupPlatform(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}},
		},
	}
	repo := &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return []PricingConfig{ch}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return nil, errors.New("group platforms failed")
		},
	}
	svc := newTestPricingConfigService(repo)

	// Look up prices through the group's configuration association.
	result, err := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err)
	require.NotNil(t, result)

	// The next read reuses the same loaded snapshot.
	result2, err2 := svc.GetPricingConfigForGroup(context.Background(), 10)
	require.NoError(t, err2)
	require.NotNil(t, result2)
}

func TestBuildCache_MultipleGroupsSamePricingConfig(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10, 20, 30},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: testPtrFloat64(15e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{
		10: "anthropic",
		20: "anthropic",
		30: "anthropic",
	})
	svc := newTestPricingConfigService(repo)

	for _, gid := range []int64{10, 20, 30} {
		result := svc.GetConfigModelPricing(context.Background(), gid, "claude-opus-4")
		require.NotNil(t, result, "group %d should have pricing", gid)
		require.Equal(t, int64(100), result.ID)
	}
}

func TestBuildCache_UnifiedModelSpace(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10, 20},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}},
			{ID: 200, Models: []string{"gpt-5.1"}},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{
		10: "anthropic",
		20: "openai",
	})
	svc := newTestPricingConfigService(repo)

	require.NotNil(t, svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4"))
	require.NotNil(t, svc.GetConfigModelPricing(context.Background(), 10, "gpt-5.1"))

	require.NotNil(t, svc.GetConfigModelPricing(context.Background(), 20, "gpt-5.1"))
	require.NotNil(t, svc.GetConfigModelPricing(context.Background(), 20, "claude-opus-4"))
}

func TestBuildCache_WildcardPreservesConfigOrder(t *testing.T) {
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			// Configuration order: shortest prefix first
			{ID: 100, Models: []string{"c-*"}, InputPrice: testPtrFloat64(1e-6)},
			{ID: 200, Models: []string{"c-son-*"}, InputPrice: testPtrFloat64(2e-6)},
			{ID: 300, Models: []string{"c-son-4-*"}, InputPrice: testPtrFloat64(3e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: "anthropic"})
	svc := newTestPricingConfigService(repo)

	// "c-son-4-xxx" matches all three wildcards, but "c-*" (ID=100) is first in config
	result := svc.GetConfigModelPricing(context.Background(), 10, "c-son-4-xxx")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)

	// "c-son-yyy" matches "c-*" and "c-son-*", but "c-*" (ID=100) is first
	result = svc.GetConfigModelPricing(context.Background(), 10, "c-son-yyy")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)

	// "c-other" only matches "c-*" (ID=100)
	result = svc.GetConfigModelPricing(context.Background(), 10, "c-other")
	require.NotNil(t, result)
	require.Equal(t, int64(100), result.ID)
}

func TestInvalidateCache(t *testing.T) {
	callCount := 0
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}},
		},
	}
	repo := &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			callCount++
			return []PricingConfig{ch}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return map[int64]string{10: "anthropic"}, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	// First load
	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)
	require.Equal(t, 1, callCount)

	// Second call should use cache
	result = svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)
	require.Equal(t, 1, callCount) // no new DB call

	// Invalidate
	svc.invalidateCache()

	// Invalidation rebuilds immediately; this read reuses the refreshed snapshot.
	result = svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.NotNil(t, result)
	require.Equal(t, 2, callCount) // rebuilt
}

func TestCreate_Success(t *testing.T) {
	createdID := int64(42)
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		getGroupsInOtherPricingConfigsFn: func(_ context.Context, _ int64, _ []int64) ([]int64, error) {
			return nil, nil
		},
		createFn: func(_ context.Context, ch *PricingConfig) error {
			ch.ID = createdID
			return nil
		},
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return &PricingConfig{ID: id, Name: "new-channel", Status: StatusActive}, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name:     "new-channel",
		GroupIDs: []int64{10},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, createdID, result.ID)
}

func TestCreate_NameExists(t *testing.T) {
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name: "existing-channel",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPricingConfigExists)
}

func TestCreate_GroupConflict(t *testing.T) {
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		getGroupsInOtherPricingConfigsFn: func(_ context.Context, _ int64, _ []int64) ([]int64, error) {
			return []int64{10}, nil // Group 10 already belongs to another pricing configuration.
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name:     "new-channel",
		GroupIDs: []int64{10, 20},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrGroupAlreadyInPricingConfig)
}

func TestCreate_DuplicateModel(t *testing.T) {
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name: "new-channel",
		ModelPricing: []ModelPricingEntry{
			{Models: []string{"claude-opus-4"}},
			{Models: []string{"claude-opus-4"}}, // duplicate
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "claude-opus-4")
}

func TestCreate_InvalidPricingIntervals(t *testing.T) {
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name: "new-channel",
		ModelPricing: []ModelPricingEntry{
			{
				Models: []string{"claude-opus-4"},
				Intervals: []PricingInterval{
					{MinTokens: 0, MaxTokens: testPtrInt(2000), InputPrice: testPtrFloat64(1e-6)},
					{MinTokens: 1000, MaxTokens: testPtrInt(3000), InputPrice: testPtrFloat64(2e-6)},
				},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "INVALID_PRICING_INTERVALS")
	require.Contains(t, err.Error(), "overlap")
}

func TestCreate_DefaultBillingModelSource(t *testing.T) {
	var capturedPricingConfig *PricingConfig
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		createFn: func(_ context.Context, ch *PricingConfig) error {
			capturedPricingConfig = ch
			ch.ID = 1
			return nil
		},
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return capturedPricingConfig, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name:               "new-channel",
		BillingModelSource: "", // empty, should default to "group_mapped"
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, BillingModelSourceGroupMapped, result.BillingModelSource)
}

func TestCreate_InvalidatesCache(t *testing.T) {
	loadCount := 0
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 100, Models: []string{"claude-opus-4"}},
		},
	}
	repo := &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			loadCount++
			return []PricingConfig{ch}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return map[int64]string{10: "anthropic"}, nil
		},
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		createFn: func(_ context.Context, c *PricingConfig) error {
			c.ID = 2
			return nil
		},
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return &PricingConfig{ID: id, Name: "new", Status: StatusActive}, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	// Load cache
	_ = svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.Equal(t, 1, loadCount)

	// Create triggers cache invalidation
	_, err := svc.Create(context.Background(), &CreatePricingConfigInput{Name: "new"})
	require.NoError(t, err)

	// Create has already rebuilt the cache; the next read reuses it.
	_ = svc.GetConfigModelPricing(context.Background(), 10, "claude-opus-4")
	require.Equal(t, 2, loadCount)
}

func TestUpdate_Success(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		updateFn: func(_ context.Context, _ *PricingConfig) error {
			return nil
		},
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return nil, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		Name:        "updated-name",
		Description: testPtrString("new desc"),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestUpdate_NotFound(t *testing.T) {
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return nil, ErrPricingConfigNotFound
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Update(context.Background(), 999, &UpdatePricingConfigInput{
		Name: "whatever",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPricingConfigNotFound)
}

func TestUpdate_NameConflict(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		existsByNameExcludingFn: func(_ context.Context, _ string, _ int64) (bool, error) {
			return true, nil // Name conflicts with another pricing configuration.
		},
	}
	svc := newTestPricingConfigService(repo)

	_, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		Name: "conflicting-name",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPricingConfigExists)
}

func TestUpdate_GroupConflict(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		getGroupsInOtherPricingConfigsFn: func(_ context.Context, _ int64, _ []int64) ([]int64, error) {
			return []int64{20}, nil // Group 20 belongs to another pricing configuration.
		},
	}
	svc := newTestPricingConfigService(repo)

	newGroupIDs := []int64{10, 20}
	_, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		GroupIDs: &newGroupIDs,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrGroupAlreadyInPricingConfig)
}

func TestUpdate_DuplicateModel(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
	}
	svc := newTestPricingConfigService(repo)

	dupPricing := []ModelPricingEntry{
		{Models: []string{"claude-opus-4"}},
		{Models: []string{"claude-opus-4"}},
	}
	_, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		ModelPricing: &dupPricing,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "claude-opus-4")
}

func TestUpdate_InvalidPricingIntervals(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
	}
	svc := newTestPricingConfigService(repo)

	invalidPricing := []ModelPricingEntry{
		{
			Models: []string{"claude-opus-4"},
			Intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: nil, InputPrice: testPtrFloat64(1e-6)},
				{MinTokens: 2000, MaxTokens: testPtrInt(4000), InputPrice: testPtrFloat64(2e-6)},
			},
		},
	}
	_, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		ModelPricing: &invalidPricing,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "INVALID_PRICING_INTERVALS")
	require.Contains(t, err.Error(), "unbounded")
}

func TestUpdate_InvalidatesPricingConfigCache(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	loadCount := 0
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		updateFn: func(_ context.Context, _ *PricingConfig) error {
			return nil
		},
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return []int64{10, 20}, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			loadCount++
			return []PricingConfig{*existing}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	// Load cache first
	_, _ = svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Equal(t, 1, loadCount)

	result, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		Description: testPtrString("updated"),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Update has already refreshed the pricing snapshot.
	_, _ = svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Equal(t, 2, loadCount)
}

func TestUpdate_InvalidatesAuthCache(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "original",
		Status: StatusActive,
	}
	auth := &mockPricingConfigAuthCacheInvalidator{}
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, _ int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		updateFn: func(_ context.Context, _ *PricingConfig) error {
			return nil
		},
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return []int64{10, 20}, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigServiceWithAuth(repo, auth)

	result, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		Description: testPtrString("updated"),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Auth cache should be invalidated for both groups
	require.ElementsMatch(t, []int64{10, 20}, auth.invalidatedGroupIDs)
}

func TestPricingConfigDelete_Success(t *testing.T) {
	deleted := false
	repo := &mockPricingConfigRepository{
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return nil, nil
		},
		deleteFn: func(_ context.Context, _ int64) error {
			deleted = true
			return nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	err := svc.Delete(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, deleted)
}

func TestPricingConfigDelete_InvalidatesCaches(t *testing.T) {
	auth := &mockPricingConfigAuthCacheInvalidator{}
	loadCount := 0
	repo := &mockPricingConfigRepository{
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return []int64{10, 20}, nil
		},
		deleteFn: func(_ context.Context, _ int64) error {
			return nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			loadCount++
			return []PricingConfig{{ID: 1, Status: StatusActive, GroupIDs: []int64{10, 20}}}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigServiceWithAuth(repo, auth)

	// Load cache first
	_, _ = svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Equal(t, 1, loadCount)

	err := svc.Delete(context.Background(), 1)
	require.NoError(t, err)

	// Auth cache invalidated for both groups
	require.ElementsMatch(t, []int64{10, 20}, auth.invalidatedGroupIDs)

	// PricingConfig cache invalidated
	_, _ = svc.GetPricingConfigForGroup(context.Background(), 10)
	require.Equal(t, 2, loadCount)
}

func TestPricingConfigDelete_NotFound(t *testing.T) {
	repo := &mockPricingConfigRepository{
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return nil, nil
		},
		deleteFn: func(_ context.Context, _ int64) error {
			return errors.New("record not found")
		},
	}
	svc := newTestPricingConfigService(repo)

	err := svc.Delete(context.Background(), 999)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestCreate_NoGroups(t *testing.T) {
	createdID := int64(55)
	getGroupsInOtherPricingConfigsCalled := false
	repo := &mockPricingConfigRepository{
		existsByNameFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		getGroupsInOtherPricingConfigsFn: func(_ context.Context, _ int64, _ []int64) ([]int64, error) {
			getGroupsInOtherPricingConfigsCalled = true
			return nil, nil
		},
		createFn: func(_ context.Context, ch *PricingConfig) error {
			ch.ID = createdID
			return nil
		},
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return &PricingConfig{ID: id, Name: "no-groups-channel", Status: StatusActive}, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.Create(context.Background(), &CreatePricingConfigInput{
		Name:     "no-groups-channel",
		GroupIDs: []int64{}, // empty slice
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, createdID, result.ID)
	// GetGroupsInOtherPricingConfigs should NOT have been called (skipped by len(input.GroupIDs) > 0)
	require.False(t, getGroupsInOtherPricingConfigsCalled)
}

func TestUpdate_StatusOnly(t *testing.T) {
	existing := &PricingConfig{
		ID:     1,
		Name:   "test-price-config",
		Status: StatusActive,
	}
	var capturedPricingConfig *PricingConfig
	repo := &mockPricingConfigRepository{
		getByIDFn: func(_ context.Context, id int64) (*PricingConfig, error) {
			return existing.Clone(), nil
		},
		updateFn: func(_ context.Context, ch *PricingConfig) error {
			capturedPricingConfig = ch
			return nil
		},
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return nil, nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	result, err := svc.Update(context.Background(), 1, &UpdatePricingConfigInput{
		Status: StatusDisabled,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	// The repository receives the updated configuration status.
	require.NotNil(t, capturedPricingConfig)
	require.Equal(t, StatusDisabled, capturedPricingConfig.Status)
	// Name should remain unchanged
	require.Equal(t, "test-price-config", capturedPricingConfig.Name)
}

func TestPricingConfigDelete_GetGroupIDsError(t *testing.T) {
	deleted := false
	repo := &mockPricingConfigRepository{
		getGroupIDsFn: func(_ context.Context, _ int64) ([]int64, error) {
			return nil, errors.New("group IDs lookup failed")
		},
		deleteFn: func(_ context.Context, _ int64) error {
			deleted = true
			return nil
		},
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return nil, nil
		},
	}
	svc := newTestPricingConfigService(repo)

	// Deletion succeeds even if reading groups for cache invalidation fails.
	err := svc.Delete(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, deleted)
}

func TestResolveGroupMappingUsesUnifiedModelNames(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-opus-4-5": "claude-opus-4-6"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformAntigravity})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-opus-4-5")
	require.True(t, result.Mapped)
	require.Equal(t, "claude-opus-4-6", result.MappedModel)
}

func TestResolveGroupMappingUsesExplicitAlias(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"alias": "anthropic-target"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformAntigravity})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "alias")
	require.True(t, result.Mapped)
	require.Equal(t, "anthropic-target", result.MappedModel)
}

func TestCheckRestrictedUsesUnifiedAllowlist(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{RestrictModels: true}

	ch := PricingConfig{
		ID:     1,
		Status: StatusActive,

		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 500, Models: []string{"shared-model"}, InputPrice: testPtrFloat64(10e-6)},
		},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformAntigravity})
	svc := newTestPricingConfigService(repo)

	restricted := svc.IsModelRestricted(context.Background(), 10, "shared-model")
	require.False(t, restricted)

	restricted = svc.IsModelRestricted(context.Background(), 10, "unknown-model")
	require.True(t, restricted, "unknown-model should be restricted for antigravity")
}

func TestGetConfigModelPricing_AntigravityOwnPricingWorks(t *testing.T) {
	// A group's prices cover both Claude and Gemini model families.
	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10},
		ModelPricing: []ModelPricingEntry{
			{ID: 600, Models: []string{"claude-*"}, InputPrice: testPtrFloat64(15e-6)},
			{ID: 601, Models: []string{"gemini-*"}, InputPrice: testPtrFloat64(2e-6)},
		},
	}
	repo := makeStandardRepo(ch, map[int64]string{10: PlatformAntigravity})
	svc := newTestPricingConfigService(repo)

	// Claude selects the matching configured price.
	result := svc.GetConfigModelPricing(context.Background(), 10, "claude-sonnet-4")
	require.NotNil(t, result)
	require.Equal(t, int64(600), result.ID)
	require.InDelta(t, 15e-6, *result.InputPrice, 1e-12)

	// Gemini selects the matching configured price.
	result = svc.GetConfigModelPricing(context.Background(), 10, "gemini-2.5-flash")
	require.NotNil(t, result)
	require.Equal(t, int64(601), result.ID)
	require.InDelta(t, 2e-6, *result.InputPrice, 1e-12)
}

func TestToUsageFields_NoMapping(t *testing.T) {
	r := GroupMappingResult{
		MappedModel:        "claude-opus-4",
		PricingConfigID:    1,
		Mapped:             false,
		BillingModelSource: BillingModelSourceRequested,
	}
	fields := r.ToUsageFields("claude-opus-4", "claude-opus-4")
	require.Equal(t, int64(1), fields.PricingConfigID)
	require.Equal(t, "claude-opus-4", fields.OriginalModel)
	require.Equal(t, "claude-opus-4", fields.GroupMappedModel)
	require.Equal(t, BillingModelSourceRequested, fields.BillingModelSource)
	require.Empty(t, fields.ModelMappingChain)
}

func TestToUsageFields_WithGroupMapping(t *testing.T) {
	r := GroupMappingResult{
		MappedModel:        "claude-sonnet-4-20250514",
		PricingConfigID:    2,
		Mapped:             true,
		BillingModelSource: BillingModelSourceGroupMapped,
	}
	fields := r.ToUsageFields("claude-sonnet-4", "claude-sonnet-4-20250514")
	require.Equal(t, int64(2), fields.PricingConfigID)
	require.Equal(t, "claude-sonnet-4", fields.OriginalModel)
	require.Equal(t, "claude-sonnet-4-20250514", fields.GroupMappedModel)
	require.Equal(t, "claude-sonnet-4→claude-sonnet-4-20250514", fields.ModelMappingChain)
}

func TestToUsageFields_WithUpstreamDifference(t *testing.T) {
	r := GroupMappingResult{
		MappedModel:        "claude-sonnet-4",
		PricingConfigID:    3,
		Mapped:             true,
		BillingModelSource: BillingModelSourceUpstream,
	}
	fields := r.ToUsageFields("my-alias", "claude-sonnet-4-20250514")
	require.Equal(t, "my-alias", fields.OriginalModel)
	require.Equal(t, "claude-sonnet-4", fields.GroupMappedModel)
	require.Equal(t, "my-alias→claude-sonnet-4→claude-sonnet-4-20250514", fields.ModelMappingChain)
}

func TestValidatePricingBillingMode(t *testing.T) {
	tests := []struct {
		name    string
		pricing []ModelPricingEntry
		wantErr bool
		errMsg  string
	}{
		{
			name:    "token mode - valid",
			pricing: []ModelPricingEntry{{BillingMode: BillingModeToken}},
		},
		{
			name: "per_request with price - valid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModePerRequest,
				PerRequestPrice: testPtrFloat64(0.5),
			}},
		},
		{
			name: "per_request with intervals - valid",
			pricing: []ModelPricingEntry{{
				BillingMode: BillingModePerRequest,
				Intervals:   []PricingInterval{{MinTokens: 0, MaxTokens: testPtrInt(1000), PerRequestPrice: testPtrFloat64(0.1)}},
			}},
		},
		{
			name:    "per_request no price no intervals - invalid",
			pricing: []ModelPricingEntry{{BillingMode: BillingModePerRequest}},
			wantErr: true,
			errMsg:  "per-request price or intervals required",
		},
		{
			name:    "image no price no intervals - invalid",
			pricing: []ModelPricingEntry{{BillingMode: BillingModeImage}},
			wantErr: true,
			errMsg:  "per-request price or intervals required",
		},
		{
			name:    "empty list - valid",
			pricing: []ModelPricingEntry{},
		},
		{
			name: "negative input_price - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode: BillingModeToken,
				InputPrice:  testPtrFloat64(-0.01),
			}},
			wantErr: true,
			errMsg:  "input_price must be >= 0",
		},
		{
			name: "negative price_multiplier - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModeToken,
				PriceMultiplier: testPtrFloat64(-1),
				InputPrice:      testPtrFloat64(0.01),
			}},
			wantErr: true,
			errMsg:  "price_multiplier must be >= 0",
		},
		{
			name: "price_multiplier without explicit price - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModeToken,
				PriceMultiplier: testPtrFloat64(1.5),
			}},
			wantErr: true,
			errMsg:  "price_multiplier requires at least one explicit price",
		},
		{
			name: "price_multiplier with interval price - valid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModeToken,
				PriceMultiplier: testPtrFloat64(1.5),
				Intervals: []PricingInterval{{
					MinTokens:  0,
					InputPrice: testPtrFloat64(0.01),
				}},
			}},
		},
		{
			name: "price_multiplier with image input price - valid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModeToken,
				PriceMultiplier: testPtrFloat64(1.5),
				ImageInputPrice: testPtrFloat64(0.01),
			}},
		},
		{
			name: "OpenAI token fast_mode_multiplier with explicit price - valid",
			pricing: []ModelPricingEntry{{
				BillingMode:        BillingModeToken,
				FastModeMultiplier: testPtrFloat64(2),
				InputPrice:         testPtrFloat64(0.01),
			}},
		},
		{
			name: "fast_mode_multiplier in unified pricing - valid",
			pricing: []ModelPricingEntry{{
				BillingMode:        BillingModeToken,
				FastModeMultiplier: testPtrFloat64(2),
				InputPrice:         testPtrFloat64(0.01),
			}},
		},
		{
			name: "fast_mode_multiplier on per-request pricing - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:        BillingModePerRequest,
				FastModeMultiplier: testPtrFloat64(2),
				PerRequestPrice:    testPtrFloat64(0.01),
			}},
			wantErr: true,
			errMsg:  "fast_mode_multiplier is only supported for token billing mode",
		},
		{
			name: "fast_mode_multiplier without explicit price - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:        BillingModeToken,
				FastModeMultiplier: testPtrFloat64(2),
			}},
			wantErr: true,
			errMsg:  "fast_mode_multiplier requires at least one explicit price",
		},
		{
			name: "negative fast_mode_multiplier - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:        BillingModeToken,
				FastModeMultiplier: testPtrFloat64(-1),
				InputPrice:         testPtrFloat64(0.01),
			}},
			wantErr: true,
			errMsg:  "fast_mode_multiplier must be >= 0",
		},
		{
			name: "interval with no price fields - invalid",
			pricing: []ModelPricingEntry{{
				BillingMode:     BillingModePerRequest,
				PerRequestPrice: testPtrFloat64(0.5),
				Intervals:       []PricingInterval{{MinTokens: 0, MaxTokens: testPtrInt(1000)}},
			}},
			wantErr: true,
			errMsg:  "has no price fields set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePricingBillingMode(tt.pricing)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateProviderStatsPricingEntries_RejectsFastModeMultiplier(t *testing.T) {
	err := (PricingConfigValidation{LoadLocation: time.LoadLocation}).ProviderStatsPricing([]ModelPricingEntry{{
		BillingMode:        BillingModeToken,
		FastModeMultiplier: testPtrFloat64(2),
		InputPrice:         testPtrFloat64(0.01),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "fast_mode_multiplier is not supported for provider stats pricing")
}

func TestValidatePricingEntries_RejectsTimePricingForNonTokenMode(t *testing.T) {
	err := (PricingConfigValidation{LoadLocation: time.LoadLocation}).PricingEntries([]ModelPricingEntry{{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: testPtrFloat64(0.05),
		TimePricing: &TimePricingConfig{
			Timezone: "Asia/Shanghai",
			Periods:  []TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
		},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "TIME_PRICING_UNSUPPORTED_MODE")
}

func TestModelPricingAcceptsTimePricing(t *testing.T) {
	err := (PricingConfigValidation{LoadLocation: time.LoadLocation}).PricingEntries([]ModelPricingEntry{{
		Models: []string{"gpt-5"},
		TimePricing: &TimePricingConfig{
			Timezone: "Asia/Shanghai",
			Periods:  []TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
		},
	}})
	require.NoError(t, err)
}

func TestResolveGroupMappingUnifiedWildcardsAcrossGroups(t *testing.T) {
	routingPolicy := GroupRoutingPolicy{ModelMapping: map[string]string{"claude-*": "claude-override", "gemini-*": "gemini-override"}}

	ch := PricingConfig{
		ID:       1,
		Status:   StatusActive,
		GroupIDs: []int64{10, 20},
	}
	repo := makePolicyRepo(routingPolicy, ch, map[int64]string{10: PlatformAntigravity, 20: PlatformAnthropic})
	svc := newTestPricingConfigService(repo)

	result := svc.ResolveGroupMapping(context.Background(), 10, "claude-opus-4")
	require.True(t, result.Mapped)
	require.Equal(t, "claude-override", result.MappedModel)

	result = svc.ResolveGroupMapping(context.Background(), 10, "gemini-2.5-pro")
	require.True(t, result.Mapped)
	require.Equal(t, "gemini-override", result.MappedModel)

	result = svc.ResolveGroupMapping(context.Background(), 20, "claude-opus-4")
	require.True(t, result.Mapped)
	require.Equal(t, "claude-override", result.MappedModel)
}

func TestGroupRoutingPolicyCreateMappingConflict(t *testing.T) {
	err := ValidateGroupRoutingPolicy(GroupRoutingPolicy{ModelMapping: map[string]string{"claude-*": "a", "claude-opus-*": "b"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "MAPPING_PATTERN_CONFLICT")
}

func TestGroupRoutingPolicyUpdateMappingConflict(t *testing.T) {
	err := ValidateGroupRoutingPolicy(GroupRoutingPolicy{ModelMapping: map[string]string{"claude-*": "a", "claude-opus-*": "b"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "MAPPING_PATTERN_CONFLICT")
}

// TestPricingConfigModelLookupIndependentOfGroupPlatform checks lookup through configuration associations.
func TestPricingConfigModelLookupIndependentOfGroupPlatform(t *testing.T) {
	config := PricingConfig{ID: 1, Status: StatusActive, GroupIDs: []int64{10, 20}, ModelPricing: []ModelPricingEntry{{Models: []string{"claude-x"}, InputPrice: testPtrFloat64(3)}, {Models: []string{"gpt-*"}, InputPrice: testPtrFloat64(5)}}}
	repo := &mockPricingConfigRepository{listAllFn: func(context.Context) ([]PricingConfig, error) { return []PricingConfig{config}, nil }, getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
		panic("pricing must not read group platforms")
	}}
	service := newTestPricingConfigService(repo)
	for _, id := range []int64{10, 20} {
		require.Equal(t, 3.0, *service.GetConfigModelPricing(context.Background(), id, "claude-x").InputPrice)
		require.Equal(t, 5.0, *service.GetConfigModelPricing(context.Background(), id, "gpt-x").InputPrice)
	}
}

func (m *mockPricingConfigRepository) Create(ctx context.Context, pricingConfig *PricingConfig) error {
	if m.createFn != nil {
		return m.createFn(ctx, pricingConfig)
	}
	return nil
}

func (m *mockPricingConfigRepository) GetByID(ctx context.Context, id int64) (*PricingConfig, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return nil, ErrPricingConfigNotFound
}

func (m *mockPricingConfigRepository) Update(ctx context.Context, pricingConfig *PricingConfig) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, pricingConfig)
	}
	return nil
}

func (m *mockPricingConfigRepository) Delete(ctx context.Context, id int64) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockPricingConfigRepository) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PricingConfig, *pagination.PaginationResult, error) {
	if m.listFn != nil {
		return m.listFn(ctx, params, status, search)
	}
	return nil, nil, nil
}

func (m *mockPricingConfigRepository) ListAll(ctx context.Context) ([]PricingConfig, error) {
	if m.listAllFn != nil {
		return m.listAllFn(ctx)
	}
	return nil, nil
}

func (m *mockPricingConfigRepository) ExistsByName(ctx context.Context, name string) (bool, error) {
	if m.existsByNameFn != nil {
		return m.existsByNameFn(ctx, name)
	}
	return false, nil
}

func (m *mockPricingConfigRepository) ExistsByNameExcluding(ctx context.Context, name string, excludeID int64) (bool, error) {
	if m.existsByNameExcludingFn != nil {
		return m.existsByNameExcludingFn(ctx, name, excludeID)
	}
	return false, nil
}

func (m *mockPricingConfigRepository) GetGroupIDs(ctx context.Context, pricingConfigID int64) ([]int64, error) {
	if m.getGroupIDsFn != nil {
		return m.getGroupIDsFn(ctx, pricingConfigID)
	}
	return nil, nil
}

func (m *mockPricingConfigRepository) SetGroupIDs(ctx context.Context, pricingConfigID int64, groupIDs []int64) error {
	if m.setGroupIDsFn != nil {
		return m.setGroupIDsFn(ctx, pricingConfigID, groupIDs)
	}
	return nil
}

func (m *mockPricingConfigRepository) GetPricingConfigIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	if m.getPricingConfigIDByGroupIDFn != nil {
		return m.getPricingConfigIDByGroupIDFn(ctx, groupID)
	}
	return 0, nil
}

func (m *mockPricingConfigRepository) GetGroupsInOtherPricingConfigs(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error) {
	if m.getGroupsInOtherPricingConfigsFn != nil {
		return m.getGroupsInOtherPricingConfigsFn(ctx, pricingConfigID, groupIDs)
	}
	return nil, nil
}

func (m *mockPricingConfigRepository) GetGroupPlatforms(ctx context.Context, groupIDs []int64) (map[int64]string, error) {
	if m.getGroupPlatformsFn != nil {
		return m.getGroupPlatformsFn(ctx, groupIDs)
	}
	return nil, nil
}

func (m *mockPricingConfigRepository) ListModelPricing(ctx context.Context, pricingConfigID int64) ([]ModelPricingEntry, error) {
	if m.listModelPricingFn != nil {
		return m.listModelPricingFn(ctx, pricingConfigID)
	}
	return nil, nil
}

func (m *mockPricingConfigRepository) CreateModelPricing(ctx context.Context, pricing *ModelPricingEntry) error {
	if m.createModelPricingFn != nil {
		return m.createModelPricingFn(ctx, pricing)
	}
	return nil
}

func (m *mockPricingConfigRepository) UpdateModelPricing(ctx context.Context, pricing *ModelPricingEntry) error {
	if m.updateModelPricingFn != nil {
		return m.updateModelPricingFn(ctx, pricing)
	}
	return nil
}

func (m *mockPricingConfigRepository) DeleteModelPricing(ctx context.Context, id int64) error {
	if m.deleteModelPricingFn != nil {
		return m.deleteModelPricingFn(ctx, id)
	}
	return nil
}

func (m *mockPricingConfigRepository) ReplaceModelPricing(ctx context.Context, pricingConfigID int64, pricingList []ModelPricingEntry) error {
	if m.replaceModelPricingFn != nil {
		return m.replaceModelPricingFn(ctx, pricingConfigID, pricingList)
	}
	return nil
}

func (m *mockPricingConfigAuthCacheInvalidator) InvalidateAuthCacheByKey(_ context.Context, key string) {
	m.invalidatedKeys = append(m.invalidatedKeys, key)
}

func (m *mockPricingConfigAuthCacheInvalidator) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	m.invalidatedUserIDs = append(m.invalidatedUserIDs, userID)
}

func (m *mockPricingConfigAuthCacheInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	m.invalidatedGroupIDs = append(m.invalidatedGroupIDs, groupID)
}

func newTestPricingConfigService(repo *mockPricingConfigRepository) *PricingConfigService {
	return NewPricingConfigService(repo, nil, PricingConfigOptions{LoadLocation: time.LoadLocation, ReadGroup: repo.readGroup})
}

func newTestPricingConfigServiceWithAuth(repo *mockPricingConfigRepository, auth *mockPricingConfigAuthCacheInvalidator) *PricingConfigService {
	return NewPricingConfigService(repo, auth, PricingConfigOptions{LoadLocation: time.LoadLocation, ReadGroup: repo.readGroup})
}

// makeStandardRepo supplies the given configuration and group-platform records.
func makeStandardRepo(ch PricingConfig, groupPlatforms map[int64]string) *mockPricingConfigRepository {
	return &mockPricingConfigRepository{
		listAllFn: func(_ context.Context) ([]PricingConfig, error) {
			return []PricingConfig{ch}, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return groupPlatforms, nil
		},
	}
}

// makePolicyRepo supplies routing policies for test group reads.
func makePolicyRepo(policy GroupRoutingPolicy, config PricingConfig, platforms map[int64]string) *mockPricingConfigRepository {
	repo := makeStandardRepo(config, platforms)
	policy.Enabled = config.IsActive()
	policy.RestrictionModelSource = config.BillingModelSource
	policy.AllowedModels = []string{}
	for _, price := range config.ModelPricing {
		policy.AllowedModels = append(policy.AllowedModels, price.Models...)
	}
	repo.readGroup = func(_ context.Context, id int64) (*Group, error) {
		if slices.Contains(config.GroupIDs, id) {
			return &Group{ID: id, RoutingPolicy: policy.Clone()}, nil
		}
		return nil, nil
	}
	return repo
}

// testPtrFloat64 creates a nullable price for test price cards.
func testPtrFloat64(value float64) *float64 { return &value }

func testPtrInt(value int) *int { return &value }

func testPtrString(value string) *string { return &value }
