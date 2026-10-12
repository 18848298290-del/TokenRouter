package routing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// GroupProvider contains the provider identity, platform, and type needed for group administration.
type GroupProvider struct {
	ID       int64
	Platform string
	Type     string
}

type GroupProviders interface {
	GetByIDs(context.Context, []int64) ([]GroupProvider, error)
	ListSchedulableByGroupID(context.Context, int64) ([]CatalogueProvider, error)
}

type GroupKeyReader interface {
	ListKeysByGroupID(context.Context, int64) ([]string, error)
}
type GroupAdminInvalidator interface {
	GroupAuthInvalidator
	InvalidateAuthCacheByKey(context.Context, string)
}
type GroupPricingInvalidator interface{ InvalidateCache() }

// GroupAdminOptions supplies model resolution, global settings, and a transaction callback for group mutations.
type GroupAdminOptions struct {
	ModelResolver RequestableResolver
	DefaultModels func(string) []string
	GlobalWeights func(context.Context) (policy.ScoreWeights, error)
	Mutate        func(context.Context, func(context.Context) error) error
}

// GroupAdmin manages and duplicates groups using injected repositories and cache invalidators.
type GroupAdmin struct {
	groupRepo                     GroupRepository
	groupDuplicateRepo            GroupDuplicateRepository
	groupSortOrderRepo            GroupSortOrderRepository
	providerRepo                  GroupProviders
	apiKeyRepo                    GroupKeyReader
	authCacheInvalidator          GroupAdminInvalidator
	pricingConfigCacheInvalidator GroupPricingInvalidator
	options                       GroupAdminOptions
}

func NewGroupAdmin(repo GroupRepository, duplicate GroupDuplicateRepository, sortOrder GroupSortOrderRepository, providers GroupProviders, keys GroupKeyReader, invalidator GroupAdminInvalidator, pricingConfigs GroupPricingInvalidator, options GroupAdminOptions) *GroupAdmin {
	return &GroupAdmin{groupRepo: repo, groupDuplicateRepo: duplicate, groupSortOrderRepo: sortOrder, providerRepo: providers, apiKeyRepo: keys, authCacheInvalidator: invalidator, pricingConfigCacheInvalidator: pricingConfigs, options: options}
}

// ValidateAdvancedOverrides checks local fields before reading global weights, which it reads only when
// weights are overridden.
func (s *GroupAdmin) ValidateAdvancedOverrides(ctx context.Context, overrides GroupAdvancedSchedulerOverrides) error {
	if err := policy.ValidateGroupOverrides(overrides); err != nil {
		return err
	}
	if !policy.HasWeightOverrides(overrides) {
		return nil
	}
	weights, err := s.options.GlobalWeights(ctx)
	if err != nil {
		return err
	}
	return policy.ValidateEffectiveWeights(policy.ApplyGroupWeightOverrides(weights, overrides))
}
